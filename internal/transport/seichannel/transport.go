// Package seichannel provides a byte transport over H264 SEI messages.
package seichannel

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openlibrecommunity/olcrtc/internal/engine"
	enginebuiltin "github.com/openlibrecommunity/olcrtc/internal/engine/builtin"
	"github.com/openlibrecommunity/olcrtc/internal/transport"
	"github.com/openlibrecommunity/olcrtc/internal/transport/common"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
)

const (
	defaultMaxPayloadSize             = 7 * 1024
	defaultMaxPayloadFragments        = 3
	defaultFragmentSize               = 900
	defaultAckTimeout                 = 3 * time.Second
	defaultFrameInterval              = 50 * time.Millisecond
	defaultFPS                        = 20
	defaultBatchSize                  = 1
	// mtslink HTTP bootstrap per lane can take 10-25 s in real-world conditions.
	// 45 s gives enough margin without blocking reconnect loops for too long.
	defaultConnectTimeout = 45 * time.Second
	maxSendAttempts                   = 8
	sampleBuilderMaxLate              = 128
	protocolMagic              uint32 = 0x4f564331 // OVC1
	protocolVersion            byte   = 1
	protocolVersionLane        byte   = 2
	frameTypeData              byte   = 1
	frameTypeAck               byte   = 2
	frameTypeHello             byte   = 3
)

var (
	// ErrVideoTrackUnsupported is returned when a carrier cannot expose video tracks.
	ErrVideoTrackUnsupported = errors.New("carrier does not support video tracks")
	// ErrAckTimeout is returned when the peer does not acknowledge a payload in time.
	ErrAckTimeout = errors.New("seichannel ack timeout")
	// ErrTransportClosed is returned when operations are attempted on a closed transport.
	ErrTransportClosed = errors.New("seichannel transport closed")
	// ErrFrameTooShort is returned when the received frame is too short to decode.
	ErrFrameTooShort = errors.New("frame too short")
	// ErrUnexpectedMagic is returned when the frame magic bytes do not match.
	ErrUnexpectedMagic = errors.New("unexpected frame magic")
	// ErrUnexpectedVersion is returned when the frame protocol version does not match.
	ErrUnexpectedVersion = errors.New("unexpected frame version")
	// ErrAckTooShort is returned when the ack frame is shorter than expected.
	ErrAckTooShort = errors.New("ack frame too short")
	// ErrDataTooShort is returned when the data frame is shorter than expected.
	ErrDataTooShort = errors.New("data frame too short")
	// ErrUnexpectedFrameType is returned for unknown frame type bytes.
	ErrUnexpectedFrameType = errors.New("unexpected frame type")
)

type transportFrame struct {
	typ       byte
	laneID    uint16
	seq       uint32
	crc       uint32
	totalLen  uint32
	fragIdx   uint16
	fragTotal uint16
	payload   []byte
}

// videoSession is the subset of engine.Session + engine.VideoTrackCapable the
// seichannel transport relies on.
type videoSession interface {
	Connect(ctx context.Context) error
	Close() error
	SetReconnectCallback(cb func())
	SetShouldReconnect(fn func() bool)
	SetEndedCallback(cb func(string))
	WatchConnection(ctx context.Context)
	CanSend() bool
	AddTrack(track webrtc.TrackLocal) error
	SetTrackHandler(cb func(*webrtc.TrackRemote, *webrtc.RTPReceiver))
}

type streamTransport struct {
	stream        videoSession
	track         *webrtc.TrackLocalStaticSample
	onData        func([]byte)
	outbound      chan []byte
	outboundAck   chan []byte
	closeCh       chan struct{}
	writerDone    chan struct{}
	nextSeq       atomic.Uint32
	closed        atomic.Bool
	writerUp      atomic.Bool
	peerReady     atomic.Bool
	sendMu        sync.Mutex
	remoteReaders sync.WaitGroup
	startWriter   sync.Once
	fragAcks      *fragAckTracker
	reassembler   *common.Reassembler
	fragmentSize  int
	ackTimeout    time.Duration
	frameInterval time.Duration
	batchSize     int
	laneID        uint16
}

// New creates a seichannel transport backed by a carrier.
func New(ctx context.Context, cfg transport.Config) (transport.Transport, error) {
	opts, err := optionsFrom(cfg)
	if err != nil {
		return nil, err
	}

	session, err := enginebuiltin.Open(ctx, cfg.Carrier, enginebuiltin.Config{
		RoomURL:   cfg.RoomURL,
		Name:      cfg.Name,
		OnData:    nil,
		DNSServer: cfg.DNSServer,
		ProxyAddr: cfg.ProxyAddr,
		ProxyPort: cfg.ProxyPort,
		Engine:    cfg.Engine,
		URL:       cfg.URL,
		Token:     cfg.Token,
	})
	if err != nil {
		return nil, fmt.Errorf("open engine session: %w", err)
	}

	vt, ok := session.(engine.VideoTrackCapable)
	if !ok || !session.Capabilities().VideoTrack {
		_ = session.Close()
		return nil, ErrVideoTrackUnsupported
	}
	stream := &engineVideoSession{session: session, vt: vt}

	// Stream/track IDs must be unique per peer — Jitsi rejects session-accept
	// when msid collides with another participant in the conference.
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			Channels:    0,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		},
		"seichannel-"+common.RandomID(),
		"olcrtc-"+common.RandomID(),
	)
	if err != nil {
		return nil, fmt.Errorf("create local video track: %w", err)
	}

	opts = opts.withDefaults()
	tr := &streamTransport{
		stream:        stream,
		track:         track,
		onData:        cfg.OnData,
		outbound:      make(chan []byte, 256),
		outboundAck:   make(chan []byte, 64),
		closeCh:       make(chan struct{}),
		writerDone:    make(chan struct{}),
		fragAcks:      newFragAckTracker(),
		reassembler:   common.NewReassembler(256),
		fragmentSize:  opts.FragmentSize,
		ackTimeout:    time.Duration(opts.AckTimeoutMS) * time.Millisecond,
		frameInterval: time.Second / time.Duration(opts.FPS),
		batchSize:     opts.BatchSize,
		laneID:        opts.LaneID,
	}

	if err := stream.AddTrack(track); err != nil {
		return nil, fmt.Errorf("attach local video track: %w", err)
	}
	stream.SetTrackHandler(tr.handleRemoteTrack)

	return tr, nil
}

// Connect starts the transport connection.
func (p *streamTransport) Connect(ctx context.Context) error {
	connectCtx, cancel := context.WithTimeout(ctx, defaultConnectTimeout)
	defer cancel()

	if err := p.stream.Connect(connectCtx); err != nil {
		return fmt.Errorf("connect stream: %w", err)
	}

	p.startWriter.Do(func() {
		p.writerUp.Store(true)
		go p.writerLoop()
	})

	return nil
}

// Send transmits data through the transport with per-fragment retransmits.
func (p *streamTransport) Send(data []byte) error {
	if p.closed.Load() {
		return ErrTransportClosed
	}

	p.sendMu.Lock()
	seq := p.nextSeq.Add(1)
	crc := crc32.ChecksumIEEE(data)
	fragments := common.FragmentPayload(data, p.effectiveFragmentSize())
	waiter := p.fragAcks.Register(seq, crc, len(fragments))
	p.sendMu.Unlock()
	defer p.fragAcks.Unregister(seq)

	pending := make([]int, len(fragments))
	for i := range pending {
		pending[i] = i
	}
	ackTimeout := p.perAttemptAckTimeout(len(fragments))
	for range maxSendAttempts {
		for _, idx := range pending {
			frame := p.encodeDataFrame(seq, crc, len(data), idx, len(fragments), fragments[idx])
			if err := p.enqueueFrame(frame, false); err != nil {
				return err
			}
		}

		if ok, err := p.awaitFragments(waiter, ackTimeout); err != nil {
			return err
		} else if ok {
			return nil
		}
		pending = waiter.Pending()
		if len(pending) == 0 {
			return nil
		}
	}

	return ErrAckTimeout
}

func (p *streamTransport) awaitFragments(waiter *fragWaiter, timeout time.Duration) (bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if waiter.Done() {
			return true, nil
		}
		select {
		case <-waiter.Notify():
		case <-timer.C:
			return waiter.Done(), nil
		case <-p.closeCh:
			return false, ErrTransportClosed
		}
	}
}

func (p *streamTransport) perAttemptAckTimeout(fragments int) time.Duration {
	if fragments <= 0 {
		return p.effectiveAckTimeout()
	}
	estimated := time.Duration(fragments) * p.effectiveFrameInterval() * 3
	if estimated < p.effectiveAckTimeout() {
		return p.effectiveAckTimeout()
	}
	const maxAckTimeout = 30 * time.Second
	if estimated > maxAckTimeout {
		return maxAckTimeout
	}
	return estimated
}

// Close terminates the transport.
func (p *streamTransport) Close() error {
	if p.closed.CompareAndSwap(false, true) {
		close(p.closeCh)
		if p.writerUp.Load() {
			<-p.writerDone
		}
		p.waitRemoteReaders()
		if err := p.stream.Close(); err != nil {
			return fmt.Errorf("close stream: %w", err)
		}
	}
	return nil
}

func (p *streamTransport) waitRemoteReaders() {
	done := make(chan struct{})
	go func() {
		p.remoteReaders.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// SetReconnectCallback registers reconnect handling.
func (p *streamTransport) SetReconnectCallback(cb func()) {
	p.stream.SetReconnectCallback(cb)
}

// SetShouldReconnect configures reconnect policy.
func (p *streamTransport) SetShouldReconnect(fn func() bool) {
	p.stream.SetShouldReconnect(fn)
}

// SetEndedCallback registers end-of-session handling.
func (p *streamTransport) SetEndedCallback(cb func(string)) {
	p.stream.SetEndedCallback(cb)
}

// WatchConnection monitors connection lifecycle.
func (p *streamTransport) WatchConnection(ctx context.Context) {
	p.stream.WatchConnection(ctx)
}

// CanSend reports whether transport is ready for sending.
func (p *streamTransport) CanSend() bool {
	return !p.closed.Load() && p.peerReady.Load() && p.stream.CanSend()
}

// Features describes the current seichannel transport semantics.
func (p *streamTransport) Features() transport.Features {
	return transport.Features{
		Reliable:        true,
		Ordered:         true,
		MessageOriented: true,
		MaxPayloadSize:  p.effectiveMaxPayloadSize(),
	}
}

func (p *streamTransport) effectiveMaxPayloadSize() int {
	payloadSize := p.effectiveFragmentSize() * defaultMaxPayloadFragments
	if payloadSize > defaultMaxPayloadSize {
		return defaultMaxPayloadSize
	}
	return payloadSize
}

func (p *streamTransport) effectiveFragmentSize() int {
	if p.fragmentSize <= 0 {
		return defaultFragmentSize
	}
	return p.fragmentSize
}

func (p *streamTransport) effectiveAckTimeout() time.Duration {
	if p.ackTimeout <= 0 {
		return defaultAckTimeout
	}
	return p.ackTimeout
}

func (p *streamTransport) effectiveFrameInterval() time.Duration {
	if p.frameInterval <= 0 {
		return defaultFrameInterval
	}
	return p.frameInterval
}

func (p *streamTransport) effectiveBatchSize() int {
	if p.batchSize <= 0 {
		return defaultBatchSize
	}
	return p.batchSize
}

func (p *streamTransport) writerLoop() {
	defer close(p.writerDone)

	ticker := time.NewTicker(p.effectiveFrameInterval())
	defer ticker.Stop()

	idle := buildVideoAccessUnit(p.encodeHelloFrame())

	for {
		select {
		case <-p.closeCh:
			return
		case <-ticker.C:
			if !p.writeBatch(idle) {
				return
			}
		}
	}
}

func (p *streamTransport) writeBatch(idle []byte) bool {
	frameInterval := p.effectiveFrameInterval()
	batchSize := p.effectiveBatchSize()
	for i := range batchSize {
		payload, ok := p.nextOutboundFrame()
		if !ok {
			return false
		}
		if payload == nil {
			if i > 0 {
				return true
			}
			_ = p.track.WriteSample(media.Sample{Data: idle, Duration: frameInterval})
			return true
		}
		_ = p.track.WriteSample(media.Sample{Data: buildVideoAccessUnit(payload), Duration: frameInterval})
	}
	return true
}

func (p *streamTransport) nextOutboundFrame() ([]byte, bool) {
	select {
	case <-p.closeCh:
		return nil, false
	case payload := <-p.outboundAck:
		return payload, true
	default:
	}

	select {
	case <-p.closeCh:
		return nil, false
	case payload := <-p.outboundAck:
		return payload, true
	case payload := <-p.outbound:
		return payload, true
	default:
		return nil, true
	}
}

func (p *streamTransport) enqueueFrame(frame []byte, priority bool) error {
	if p.closed.Load() {
		return ErrTransportClosed
	}

	ch := p.outbound
	if priority {
		ch = p.outboundAck
	}

	select {
	case <-p.closeCh:
		return ErrTransportClosed
	case ch <- frame:
		return nil
	}
}

func (p *streamTransport) handleRemoteTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	p.remoteReaders.Add(1)
	go func() {
		defer p.remoteReaders.Done()
		sb := samplebuilder.New(sampleBuilderMaxLate, &codecs.H264Packet{}, track.Codec().ClockRate)

		popSamples := func() {
			for sample := sb.Pop(); sample != nil; sample = sb.Pop() {
				p.handleSample(sample.Data)
			}
		}

		for {
			if p.closed.Load() {
				sb.Flush()
				popSamples()
				return
			}
			select {
			case <-p.closeCh:
				sb.Flush()
				popSamples()
				return
			default:
			}

			_ = track.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
			packet, _, err := track.ReadRTP()
			if err != nil {
				select {
				case <-p.closeCh:
					sb.Flush()
					popSamples()
					return
				default:
				}
				if timeout, ok := err.(interface{ Timeout() bool }); ok && timeout.Timeout() {
					continue
				}
				sb.Flush()
				popSamples()
				return
			}

			sb.Push(packet)
			popSamples()
		}
	}()
}

func (p *streamTransport) handleSample(sample []byte) {
	payloads, err := extractVideoPayloads(sample)
	if err != nil {
		return
	}

	for _, payload := range payloads {
		frame, err := decodeTransportFrame(payload)
		if err != nil {
			continue
		}
		if !p.acceptFrame(frame) {
			continue
		}

		switch frame.typ {
		case frameTypeHello:
			p.peerReady.Store(true)
		case frameTypeAck:
			p.peerReady.Store(true)
			p.resolveAck(frame.seq, frame.crc, frame.fragIdx)
		case frameTypeData:
			p.peerReady.Store(true)
			p.handleInboundFrame(frame)
		}
	}
}

func (p *streamTransport) handleInboundFrame(frame transportFrame) {
	result, data := p.reassembler.Push(common.Fragment{
		Seq:       frame.seq,
		CRC:       frame.crc,
		TotalLen:  frame.totalLen,
		FragIdx:   frame.fragIdx,
		FragTotal: frame.fragTotal,
		Payload:   frame.payload,
	})
	switch result {
	case common.ResultDelivered:
		if p.onData != nil {
			p.onData(data)
		}
		p.sendAck(frame.seq, frame.crc, frame.fragIdx)
	case common.ResultPartial, common.ResultDuplicate:
		p.sendAck(frame.seq, frame.crc, frame.fragIdx)
	case common.ResultIgnore:
	}
}

func (p *streamTransport) sendAck(seq, crc uint32, fragIdx uint16) {
	_ = p.enqueueFrame(p.encodeAckFrame(seq, crc, fragIdx), true)
}

func (p *streamTransport) resolveAck(seq, crc uint32, fragIdx uint16) {
	if fragIdx == ^uint16(0) {
		p.fragAcks.MarkAll(seq, crc)
		return
	}
	p.fragAcks.Mark(seq, crc, int(fragIdx))
}

func (p *streamTransport) encodeDataFrame(seq, crc uint32, totalLen, fragIdx, fragTotal int, payload []byte) []byte {
	if p.laneID != 0 {
		return encodeLaneDataFrame(p.laneID, seq, crc, totalLen, fragIdx, fragTotal, payload)
	}
	return encodeDataFrame(seq, crc, totalLen, fragIdx, fragTotal, payload)
}

func encodeDataFrame(seq, crc uint32, totalLen, fragIdx, fragTotal int, payload []byte) []byte {
	out := make([]byte, 22+len(payload))
	binary.BigEndian.PutUint32(out[0:4], protocolMagic)
	out[4] = protocolVersion
	out[5] = frameTypeData
	binary.BigEndian.PutUint32(out[6:10], seq)
	binary.BigEndian.PutUint32(out[10:14], crc)
	binary.BigEndian.PutUint32(out[14:18], uint32(totalLen))  //nolint:gosec,lll // G115: bounded conversion verified by surrounding logic
	binary.BigEndian.PutUint16(out[18:20], uint16(fragIdx))   //nolint:gosec,lll // G115: bounded conversion verified by surrounding logic
	binary.BigEndian.PutUint16(out[20:22], uint16(fragTotal)) //nolint:gosec,lll // G115: bounded conversion verified by surrounding logic
	copy(out[22:], payload)
	return out
}

func encodeLaneDataFrame(laneID uint16, seq, crc uint32, totalLen, fragIdx, fragTotal int, payload []byte) []byte {
	out := make([]byte, 24+len(payload))
	binary.BigEndian.PutUint32(out[0:4], protocolMagic)
	out[4] = protocolVersionLane
	out[5] = frameTypeData
	binary.BigEndian.PutUint16(out[6:8], laneID)
	binary.BigEndian.PutUint32(out[8:12], seq)
	binary.BigEndian.PutUint32(out[12:16], crc)
	binary.BigEndian.PutUint32(out[16:20], uint32(totalLen))  //nolint:gosec,lll // G115: bounded conversion verified by surrounding logic
	binary.BigEndian.PutUint16(out[20:22], uint16(fragIdx))   //nolint:gosec,lll // G115: bounded conversion verified by surrounding logic
	binary.BigEndian.PutUint16(out[22:24], uint16(fragTotal)) //nolint:gosec,lll // G115: bounded conversion verified by surrounding logic
	copy(out[24:], payload)
	return out
}

func (p *streamTransport) encodeAckFrame(seq, crc uint32, fragIdx uint16) []byte {
	if p.laneID != 0 {
		return encodeLaneAckFrame(p.laneID, seq, crc, fragIdx)
	}
	return encodeAckFrame(seq, crc, fragIdx)
}

func encodeAckFrame(seq, crc uint32, fragIdx uint16) []byte {
	out := make([]byte, 16)
	binary.BigEndian.PutUint32(out[0:4], protocolMagic)
	out[4] = protocolVersion
	out[5] = frameTypeAck
	binary.BigEndian.PutUint32(out[6:10], seq)
	binary.BigEndian.PutUint32(out[10:14], crc)
	binary.BigEndian.PutUint16(out[14:16], fragIdx)
	return out
}

func encodeLaneAckFrame(laneID uint16, seq, crc uint32, fragIdx uint16) []byte {
	out := make([]byte, 18)
	binary.BigEndian.PutUint32(out[0:4], protocolMagic)
	out[4] = protocolVersionLane
	out[5] = frameTypeAck
	binary.BigEndian.PutUint16(out[6:8], laneID)
	binary.BigEndian.PutUint32(out[8:12], seq)
	binary.BigEndian.PutUint32(out[12:16], crc)
	binary.BigEndian.PutUint16(out[16:18], fragIdx)
	return out
}

func (p *streamTransport) encodeHelloFrame() []byte {
	if p.laneID != 0 {
		return encodeLaneHelloFrame(p.laneID)
	}
	return encodeHelloFrame()
}

func encodeHelloFrame() []byte {
	out := make([]byte, 6)
	binary.BigEndian.PutUint32(out[0:4], protocolMagic)
	out[4] = protocolVersion
	out[5] = frameTypeHello
	return out
}

func encodeLaneHelloFrame(laneID uint16) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint32(out[0:4], protocolMagic)
	out[4] = protocolVersionLane
	out[5] = frameTypeHello
	binary.BigEndian.PutUint16(out[6:8], laneID)
	return out
}

func (p *streamTransport) acceptFrame(frame transportFrame) bool {
	return frame.laneID == p.laneID
}

func decodeTransportFrame(data []byte) (transportFrame, error) {
	if len(data) < 6 {
		return transportFrame{}, ErrFrameTooShort
	}
	if binary.BigEndian.Uint32(data[0:4]) != protocolMagic {
		return transportFrame{}, ErrUnexpectedMagic
	}
	if data[4] != protocolVersion && data[4] != protocolVersionLane {
		return transportFrame{}, ErrUnexpectedVersion
	}

	frame := transportFrame{typ: data[5]}
	if data[4] == protocolVersionLane {
		return decodeLaneTransportFrame(data, frame)
	}
	switch frame.typ {
	case frameTypeHello:
		return frame, nil
	case frameTypeAck:
		if len(data) < 14 {
			return transportFrame{}, ErrAckTooShort
		}
		frame.seq = binary.BigEndian.Uint32(data[6:10])
		frame.crc = binary.BigEndian.Uint32(data[10:14])
		if len(data) >= 16 {
			frame.fragIdx = binary.BigEndian.Uint16(data[14:16])
		} else {
			frame.fragIdx = ^uint16(0)
		}
		return frame, nil
	case frameTypeData:
		if len(data) < 22 {
			return transportFrame{}, ErrDataTooShort
		}
		frame.seq = binary.BigEndian.Uint32(data[6:10])
		frame.crc = binary.BigEndian.Uint32(data[10:14])
		frame.totalLen = binary.BigEndian.Uint32(data[14:18])
		frame.fragIdx = binary.BigEndian.Uint16(data[18:20])
		frame.fragTotal = binary.BigEndian.Uint16(data[20:22])
		frame.payload = append([]byte(nil), data[22:]...)
		return frame, nil
	default:
		return transportFrame{}, ErrUnexpectedFrameType
	}
}

func decodeLaneTransportFrame(data []byte, frame transportFrame) (transportFrame, error) {
	if len(data) < 8 {
		return transportFrame{}, ErrFrameTooShort
	}
	frame.laneID = binary.BigEndian.Uint16(data[6:8])
	switch frame.typ {
	case frameTypeHello:
		return frame, nil
	case frameTypeAck:
		if len(data) < 18 {
			return transportFrame{}, ErrAckTooShort
		}
		frame.seq = binary.BigEndian.Uint32(data[8:12])
		frame.crc = binary.BigEndian.Uint32(data[12:16])
		frame.fragIdx = binary.BigEndian.Uint16(data[16:18])
		return frame, nil
	case frameTypeData:
		if len(data) < 24 {
			return transportFrame{}, ErrDataTooShort
		}
		frame.seq = binary.BigEndian.Uint32(data[8:12])
		frame.crc = binary.BigEndian.Uint32(data[12:16])
		frame.totalLen = binary.BigEndian.Uint32(data[16:20])
		frame.fragIdx = binary.BigEndian.Uint16(data[20:22])
		frame.fragTotal = binary.BigEndian.Uint16(data[22:24])
		frame.payload = append([]byte(nil), data[24:]...)
		return frame, nil
	default:
		return transportFrame{}, ErrUnexpectedFrameType
	}
}
