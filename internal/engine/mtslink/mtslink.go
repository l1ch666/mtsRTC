package mtslink

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	authMTSLink "github.com/openlibrecommunity/olcrtc/internal/auth/mtslink"
	"github.com/openlibrecommunity/olcrtc/internal/engine"
	"github.com/openlibrecommunity/olcrtc/internal/logger"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/intervalpli"
	"github.com/pion/webrtc/v4"
)

const (
	defaultSendQueueSize = 16
	defaultGuestName     = "olcrtc-mtslink"
)

var (
	ErrSessionClosed       = errors.New("mtslink session closed")
	ErrByteStreamDisabled  = errors.New("mtslink byte stream unsupported; use videochannel")
	ErrSessionIDRequired   = errors.New("mtslink session id required")
	ErrUserIDRequired      = errors.New("mtslink user id required")
	ErrJoinTokenRequired   = errors.New("mtslink join token required")
	ErrPublishTokenMissing = errors.New("mtslink publish token required")
)

type Session struct {
	name         string
	roomURL      string
	sessionID    string
	userID       string
	joinToken    string
	publishToken string
	conferenceID string
	did          string
	pid          string

	client *authMTSLink.Client

	pcMu sync.Mutex
	pc   *webrtc.PeerConnection

	videoTrackMu sync.RWMutex
	videoTracks  []webrtc.TrackLocal
	onVideoTrack func(*webrtc.TrackRemote, *webrtc.RTPReceiver)

	onReconnect     func(*webrtc.DataChannel)
	shouldReconnect func() bool
	onEnded         func(string)

	sendQueue chan []byte
	done      chan struct{}
	cancel    context.CancelFunc

	connected atomic.Bool
	closed    atomic.Bool
	closeOnce sync.Once
	wg        sync.WaitGroup
}

func New(_ context.Context, cfg engine.Config) (engine.Session, error) {
	extra := cfg.Extra
	if extra == nil {
		extra = map[string]string{}
	}
	sessionID := extra[authMTSLink.ExtraSessionID]
	if sessionID == "" {
		return nil, ErrSessionIDRequired
	}
	userID := extra[authMTSLink.ExtraUserID]
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	joinToken := extra[authMTSLink.ExtraJoinToken]
	if joinToken == "" {
		joinToken = cfg.Token
	}
	if joinToken == "" {
		return nil, ErrJoinTokenRequired
	}
	publishToken := extra[authMTSLink.ExtraPublishToken]
	if publishToken == "" {
		return nil, ErrPublishTokenMissing
	}
	name := cfg.Name
	if name == "" {
		name = defaultGuestName
	}
	_, cancel := context.WithCancel(context.Background())
	client := authMTSLink.NewClient(os.Getenv("MTS_COOKIE"))
	client.GuestName = name
	return &Session{
		name:         name,
		roomURL:      extra[authMTSLink.ExtraRoomURL],
		sessionID:    sessionID,
		userID:       userID,
		joinToken:    joinToken,
		publishToken: publishToken,
		conferenceID: extra[authMTSLink.ExtraConferenceID],
		did:          extra[authMTSLink.ExtraDID],
		pid:          extra[authMTSLink.ExtraPID],
		client:       client,
		sendQueue:    make(chan []byte, defaultSendQueueSize),
		done:         make(chan struct{}),
		cancel:       cancel,
	}, nil
}

func (s *Session) Capabilities() engine.Capabilities {
	return engine.Capabilities{VideoTrack: true}
}

func (s *Session) Connect(ctx context.Context) error {
	if s.closed.Load() {
		return ErrSessionClosed
	}

	pc, err := s.newPeerConnection()
	if err != nil {
		return err
	}

	s.videoTrackMu.RLock()
	for _, track := range s.videoTracks {
		if _, err := pc.AddTrack(track); err != nil {
			s.videoTrackMu.RUnlock()
			_ = pc.Close()
			return fmt.Errorf("mtslink add local video track: %w", err)
		}
	}
	s.videoTrackMu.RUnlock()

	if _, err := pc.AddTransceiverFromKind(
		webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv},
	); err != nil {
		_ = pc.Close()
		return fmt.Errorf("mtslink add video transceiver: %w", err)
	}

	pc.OnTrack(func(track *webrtc.TrackRemote, recv *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeVideo {
			return
		}
		if cb := s.videoTrackHandler(); cb != nil {
			cb(track, recv)
		}
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		logger.Debugf("mtslink pc state: %s", state.String())
		switch state {
		case webrtc.PeerConnectionStateConnected:
			s.connected.Store(true)
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateDisconnected:
			s.connected.Store(false)
			if !s.closed.Load() && s.onEnded != nil {
				s.onEnded("mtslink peer connection " + state.String())
			}
		}
	})

	offer, err := s.localOffer(ctx, pc)
	if err != nil {
		_ = pc.Close()
		return err
	}

	bootstrap := authMTSLink.Bootstrap{
		Room:         authMTSLink.RoomInfo{RawURL: s.roomURL, SessionID: s.sessionID},
		UserID:       s.userID,
		JoinToken:    s.joinToken,
		PublishToken: s.publishToken,
		ConferenceID: s.conferenceID,
		DID:          s.did,
		PID:          s.pid,
	}
	answer, peerID, err := s.client.JoinSFU(ctx, bootstrap, offer, s.name)
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("mtslink sfu join: %w", err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		_ = pc.Close()
		return fmt.Errorf("mtslink set answer: %w", err)
	}

	s.pcMu.Lock()
	old := s.pc
	s.pc = pc
	s.pcMu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	_ = s.client.AudioVideoControl(ctx, s.conferenceID, true)
	s.wg.Add(1)
	go s.pinLoop(peerID)
	logger.Infof("mtslink: joined session=%s peer=%s", s.sessionID, peerID)
	return nil
}

func (s *Session) newPeerConnection() (*webrtc.PeerConnection, error) {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}
	registry := &interceptor.Registry{}
	intervalPliFactory, err := intervalpli.NewReceiverInterceptor()
	if err != nil {
		return nil, err
	}
	registry.Add(intervalPliFactory)
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, registry); err != nil {
		return nil, err
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithInterceptorRegistry(registry))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, fmt.Errorf("mtslink new peer connection: %w", err)
	}
	return pc, nil
}

func (s *Session) localOffer(ctx context.Context, pc *webrtc.PeerConnection) (string, error) {
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return "", fmt.Errorf("mtslink create offer: %w", err)
	}
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return "", fmt.Errorf("mtslink set local offer: %w", err)
	}
	select {
	case <-gatherComplete:
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(8 * time.Second):
		logger.Debugf("mtslink ICE gather timeout; continuing with partial offer")
	}
	ld := pc.LocalDescription()
	if ld == nil {
		return "", errors.New("mtslink missing local description")
	}
	return ld.SDP, nil
}

func (s *Session) pinLoop(peerID string) {
	defer s.wg.Done()
	if peerID == "" {
		return
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			if err := s.client.Pin(context.Background(), peerID); err != nil {
				logger.Debugf("mtslink pin: %v", err)
			}
		}
	}
}

func (s *Session) Send([]byte) error { return ErrByteStreamDisabled }

func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.connected.Store(false)
		if s.cancel != nil {
			s.cancel()
		}
		close(s.done)
		s.pcMu.Lock()
		pc := s.pc
		s.pc = nil
		s.pcMu.Unlock()
		if pc != nil {
			_ = pc.Close()
		}
		_ = s.client.AudioVideoControl(context.Background(), s.conferenceID, false)
		stopped := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
		}
	})
	return nil
}

func (s *Session) SetReconnectCallback(cb func(*webrtc.DataChannel)) { s.onReconnect = cb }
func (s *Session) SetShouldReconnect(fn func() bool)                 { s.shouldReconnect = fn }
func (s *Session) SetEndedCallback(cb func(string))                  { s.onEnded = cb }

func (s *Session) WatchConnection(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-s.done:
	}
}

func (s *Session) CanSend() bool {
	return !s.closed.Load() && s.connected.Load()
}

func (s *Session) GetSendQueue() chan []byte { return s.sendQueue }
func (s *Session) GetBufferedAmount() uint64 { return 0 }

func (s *Session) AddVideoTrack(track webrtc.TrackLocal) error {
	s.videoTrackMu.Lock()
	s.videoTracks = append(s.videoTracks, track)
	s.videoTrackMu.Unlock()

	s.pcMu.Lock()
	pc := s.pc
	s.pcMu.Unlock()
	if pc == nil {
		return nil
	}
	if _, err := pc.AddTrack(track); err != nil {
		return fmt.Errorf("mtslink add video track: %w", err)
	}
	return nil
}

func (s *Session) SetVideoTrackHandler(cb func(*webrtc.TrackRemote, *webrtc.RTPReceiver)) {
	s.videoTrackMu.Lock()
	defer s.videoTrackMu.Unlock()
	s.onVideoTrack = cb
}

func (s *Session) videoTrackHandler() func(*webrtc.TrackRemote, *webrtc.RTPReceiver) {
	s.videoTrackMu.RLock()
	defer s.videoTrackMu.RUnlock()
	return s.onVideoTrack
}

func init() { //nolint:gochecknoinits // engine registration is the canonical Go pattern for plugins
	engine.Register("mtslink", New)
}
