package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openlibrecommunity/olcrtc/internal/control"
	"github.com/openlibrecommunity/olcrtc/internal/handshake"
	"github.com/openlibrecommunity/olcrtc/internal/logger"
	"github.com/openlibrecommunity/olcrtc/internal/muxconn"
	"github.com/openlibrecommunity/olcrtc/internal/names"
	"github.com/openlibrecommunity/olcrtc/internal/runtime"
	"github.com/openlibrecommunity/olcrtc/internal/transport"
	"github.com/openlibrecommunity/olcrtc/internal/transport/seichannel"
	"github.com/xtaci/smux"
)

type serverLane struct {
	id          int
	protocolID  uint16
	ln          transport.Transport
	conn        *muxconn.Conn
	session     *smux.Session
	controlStrm *smux.Stream
	controlStop context.CancelFunc
	sessionID   string
	deviceID    string
	ready       bool
	mu          sync.RWMutex
}

type serverLanePool struct {
	cfg   runtime.MultipathConfig
	lanes []*serverLane
}

func serverMultipathEnabled(cfg Config) bool {
	return strings.EqualFold(cfg.Carrier, "mtslink") &&
		strings.EqualFold(cfg.Transport, "seichannel") &&
		cfg.Multipath.WithDefaults().Enabled()
}

func (s *Server) bringUpMultipath(ctx context.Context, cfg Config, cancel context.CancelFunc) error {
	mp := cfg.Multipath.WithDefaults()
	pool := &serverLanePool{
		cfg:   mp,
		lanes: make([]*serverLane, mp.Lanes),
	}

	logger.Infof("Connecting transport=%s carrier=%s multipath_lanes=%d ...", cfg.Transport, cfg.Carrier, mp.Lanes)
	sem := make(chan struct{}, mp.ConnectParallelism)
	errCh := make(chan error, mp.Lanes)
	var wg sync.WaitGroup
	for i := 0; i < mp.Lanes; i++ {
		lane := &serverLane{id: i, protocolID: uint16(i + 1)}
		pool.lanes[i] = lane
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			}
			if err := s.openServerLane(ctx, cfg, cancel, lane, mp); err != nil {
				errCh <- fmt.Errorf("lane %d: %w", lane.id+1, err)
			}
		}()
	}
	wg.Wait()
	close(errCh)

	ready := pool.readyCount()
	if ready < mp.MinReady {
		closeServerLanes(pool, s.onClose, "closed")
		var first error
		for err := range errCh {
			if first == nil {
				first = err
			}
			logger.Warnf("server multipath lane setup failed: %v", err)
		}
		if first == nil {
			first = fmt.Errorf("ready lanes below minimum: ready=%d min=%d", ready, mp.MinReady)
		}
		return fmt.Errorf("multipath setup failed: %w", first)
	}

	s.lanePool = pool
	logger.Infof("Link connected (multipath ready lanes=%d/%d control_lanes=%d min_ready=%d)",
		ready, mp.Lanes, mp.ControlLanes, mp.MinReady)
	return nil
}

func (s *Server) openServerLane(
	ctx context.Context,
	cfg Config,
	cancel context.CancelFunc,
	lane *serverLane,
	mp runtime.MultipathConfig,
) error {
	ln, err := transport.New(ctx, cfg.Transport, transport.Config{
		Carrier:    cfg.Carrier,
		RoomURL:    cfg.RoomURL,
		Engine:     cfg.Engine,
		URL:        cfg.URL,
		Token:      cfg.Token,
		ChannelID:  cfg.ChannelID,
		DeviceID:   fmt.Sprintf("server-lane-%02d", lane.id+1),
		Name:       fmt.Sprintf("%s-%02d", names.Generate(), lane.id+1),
		OnData:     lane.onData,
		DNSServer:  s.dnsServer,
		ProxyAddr:  s.socksProxyAddr,
		ProxyPort:  s.socksProxyPort,
		Options:    serverLaneOptions(cfg.TransportOptions, lane.protocolID),
		Traffic:    cfg.Traffic,
		OnPeerData: s.onPeerData,
	})
	if err != nil {
		return fmt.Errorf("failed to create transport: %w", err)
	}

	lane.ln = ln
	ln.SetEndedCallback(func(reason string) {
		lane.mu.Lock()
		lane.ready = false
		lane.mu.Unlock()
		logger.Infof("Server multipath lane=%d reported conference end: %s", lane.id+1, reason)
		if lane.id < mp.ControlLanes {
			cancel()
		}
	})
	ln.SetShouldReconnect(func() bool { return ctx.Err() == nil })
	ln.SetReconnectCallback(func() {
		if ctx.Err() != nil {
			return
		}
		lane.mu.RLock()
		sess := lane.session
		lane.mu.RUnlock()
		s.recordReconnect()
		logger.Infof("server reconnect reason=carrier lane=%d - reinstalling smux session", lane.id+1)
		resetServerLanePeer(lane)
		s.reinstallLaneSession(lane, sess, "reconnect")
	})

	if err := s.installLaneSession(lane); err != nil {
		_ = ln.Close()
		return err
	}

	if err := ln.Connect(ctx); err != nil {
		closeServerLane(lane, s.onClose, "closed")
		return fmt.Errorf("failed to connect link: %w", err)
	}

	lane.mu.Lock()
	lane.ready = true
	lane.mu.Unlock()

	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		ln.WatchConnection(ctx)
	}()
	go func() {
		defer s.wg.Done()
		s.serveLane(ctx, lane)
	}()
	return nil
}

func (s *Server) installLaneSession(lane *serverLane) error {
	conn := muxconn.New(lane.ln, s.cipher)
	sess, err := smux.Server(conn, smuxConfig(linkMaxPayload(lane.ln)))
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smux server init failed: %w", err)
	}
	lane.mu.Lock()
	lane.conn = conn
	lane.session = sess
	lane.mu.Unlock()
	return nil
}

func serverLaneOptions(base transport.Options, protocolID uint16) transport.Options {
	if opts, ok := base.(seichannel.Options); ok {
		opts.LaneID = protocolID
		return opts
	}
	return seichannel.Options{LaneID: protocolID}
}

func (l *serverLane) onData(data []byte) {
	l.mu.RLock()
	conn := l.conn
	l.mu.RUnlock()
	if conn != nil {
		conn.Push(data)
	}
}

func (s *Server) serveLane(ctx context.Context, lane *serverLane) {
	for {
		if contextDone(ctx) {
			return
		}
		lane.mu.RLock()
		sess := lane.session
		lane.mu.RUnlock()
		if sess == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
				continue
			}
		}

		if !lane.handshakeReady() {
			if !s.acceptLaneHandshake(ctx, lane, sess) {
				continue
			}
		}

		stream, err := sess.AcceptStream()
		if err != nil {
			if contextDone(ctx) {
				return
			}
			logger.Debugf("AcceptStream lane=%d returned %v - reinstalling session", lane.id+1, err)
			s.reinstallLaneSession(lane, sess, "reconnect")
			continue
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleStream(ctx, stream, lane.currentSessionID())
		}()
	}
}

func (l *serverLane) handshakeReady() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.sessionID != ""
}

func (l *serverLane) currentSessionID() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.sessionID
}

func (s *Server) acceptLaneHandshake(ctx context.Context, lane *serverLane, sess *smux.Session) bool {
	stream, err := sess.AcceptStream()
	if err != nil {
		if contextDone(ctx) {
			return false
		}
		logger.Debugf("AcceptStream(control lane=%d) returned %v - reinstalling session", lane.id+1, err)
		resetServerLanePeer(lane)
		s.reinstallLaneSession(lane, sess, "reconnect")
		return false
	}
	_ = stream.SetDeadline(time.Now().Add(handshake.DefaultTimeout))
	hello, sid, err := handshake.Server(stream, s.authHook)
	_ = stream.SetDeadline(time.Time{})
	if err != nil {
		logger.Warnf("handshake failed lane=%d: %v", lane.id+1, err)
		_ = stream.Close()
		resetServerLanePeer(lane)
		s.reinstallLaneSession(lane, sess, "reconnect")
		return false
	}

	lane.mu.Lock()
	lane.deviceID = hello.DeviceID
	lane.sessionID = sid
	lane.controlStrm = stream
	lane.mu.Unlock()

	s.recordSession(sid)
	s.onOpen(sid, hello.DeviceID, hello.Claims)
	logger.Infof("session %s opened (device=%s lane=%d)", sid, hello.DeviceID, lane.id+1)
	s.startLaneControlLoop(ctx, lane, sess, stream)
	return true
}

func (s *Server) startLaneControlLoop(ctx context.Context, lane *serverLane, sess *smux.Session, stream *smux.Stream) {
	controlCtx, stop := context.WithCancel(ctx)
	lane.mu.Lock()
	lane.controlStrm = stream
	lane.controlStop = stop
	lane.mu.Unlock()

	liveness := s.liveness
	onPong := liveness.OnPong
	onMissedPong := liveness.OnMissedPong
	onUnhealthy := liveness.OnUnhealthy
	liveness.OnPong = func(h control.Health) {
		s.recordPong(h)
		logger.Debugf("control alive session=%s lane=%d rtt=%v seq=%d", lane.currentSessionID(), lane.id+1, h.RTT, h.Seq)
		if onPong != nil {
			onPong(h)
		}
	}
	liveness.OnMissedPong = func(missed int) {
		s.recordMissed(missed)
		logger.Warnf("control missed pong on server lane=%d: missed_pongs=%d", lane.id+1, missed)
		if onMissedPong != nil {
			onMissedPong(missed)
		}
	}
	liveness.OnUnhealthy = func(missed int) {
		s.recordUnhealthy(missed)
		logger.Warnf("control stream unhealthy on server lane=%d: missed_pongs=%d", lane.id+1, missed)
		if onUnhealthy != nil {
			onUnhealthy(missed)
		}
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { _ = stream.Close() }()
		err := control.Run(controlCtx, stream, liveness)
		if controlCtx.Err() != nil || ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.Warnf("server control stream ended lane=%d: %v", lane.id+1, err)
		}
		s.recordReconnect()
		logger.Infof("server reconnect reason=liveness lane=%d - reinstalling smux session", lane.id+1)
		resetServerLanePeer(lane)
		s.reinstallLaneSession(lane, sess, "reconnect")
	}()
}

func resetServerLanePeer(lane *serverLane) {
	lane.mu.RLock()
	ln := lane.ln
	lane.mu.RUnlock()
	if resetter, ok := ln.(interface{ ResetPeer() }); ok {
		resetter.ResetPeer()
	}
}

func (s *Server) reinstallLaneSession(lane *serverLane, dead *smux.Session, reason string) {
	s.reinstallMu.Lock()
	defer s.reinstallMu.Unlock()

	newConn := muxconn.New(lane.ln, s.cipher)
	newSess, err := smux.Server(newConn, smuxConfig(linkMaxPayload(lane.ln)))
	if err != nil {
		logger.Warnf("smux server init failed lane=%d: %v", lane.id+1, err)
		_ = newConn.Close()
		return
	}

	lane.mu.Lock()
	if lane.session != dead {
		lane.mu.Unlock()
		_ = newSess.Close()
		_ = newConn.Close()
		return
	}
	oldSess := lane.session
	oldConn := lane.conn
	oldControl := lane.controlStrm
	oldControlStop := lane.controlStop
	oldSID := lane.sessionID
	lane.session = newSess
	lane.conn = newConn
	lane.controlStrm = nil
	lane.controlStop = nil
	lane.sessionID = ""
	lane.deviceID = ""
	lane.ready = true
	lane.mu.Unlock()

	if oldControlStop != nil {
		oldControlStop()
	}
	if oldSess != nil {
		_ = oldSess.Close()
	}
	if oldConn != nil {
		_ = oldConn.Close()
	}
	if oldControl != nil {
		_ = oldControl.Close()
	}
	if oldSID != "" {
		s.onClose(oldSID, reason)
	}
}

func (p *serverLanePool) readyCount() int {
	if p == nil {
		return 0
	}
	ready := 0
	for _, lane := range p.lanes {
		if lane == nil {
			continue
		}
		lane.mu.RLock()
		ok := lane.ready
		lane.mu.RUnlock()
		if ok {
			ready++
		}
	}
	return ready
}

func (s *Server) closeMultipathSessions(reason string) {
	pool := s.lanePool
	s.lanePool = nil
	closeServerLanes(pool, s.onClose, reason)
}

func closeServerLanes(pool *serverLanePool, onClose SessionCloseFunc, reason string) {
	if pool == nil {
		return
	}
	for _, lane := range pool.lanes {
		closeServerLane(lane, onClose, reason)
	}
}

func closeServerLane(lane *serverLane, onClose SessionCloseFunc, reason string) {
	if lane == nil {
		return
	}
	lane.mu.Lock()
	controlStream := lane.controlStrm
	controlStop := lane.controlStop
	sess := lane.session
	conn := lane.conn
	ln := lane.ln
	oldSID := lane.sessionID
	lane.controlStrm = nil
	lane.controlStop = nil
	lane.session = nil
	lane.conn = nil
	lane.sessionID = ""
	lane.deviceID = ""
	lane.ready = false
	lane.mu.Unlock()

	if controlStop != nil {
		controlStop()
	}
	notifyControlClose(controlStream)
	if sess != nil {
		_ = sess.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
	if ln != nil {
		_ = ln.Close()
	}
	if controlStream != nil {
		_ = controlStream.Close()
	}
	if oldSID != "" {
		onClose(oldSID, reason)
	}
}
