package client

import (
	"context"
	"fmt"
	"math"
	"net"
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

func clientMultipathEnabled(cfg Config) bool {
	return strings.EqualFold(cfg.Carrier, "mtslink") &&
		strings.EqualFold(cfg.Transport, "seichannel") &&
		cfg.Multipath.WithDefaults().Enabled()
}

func (c *Client) bringUpMultipath(ctx context.Context, cfg Config, cancel context.CancelFunc) error {
	mp := cfg.Multipath.WithDefaults()
	pool := &clientLanePool{
		cfg:   mp,
		lanes: make([]*clientLane, mp.Lanes),
	}

	sem := make(chan struct{}, mp.ConnectParallelism)
	errCh := make(chan error, mp.Lanes)
	var wg sync.WaitGroup
	for i := 0; i < mp.Lanes; i++ {
		lane := &clientLane{id: i, protocolID: uint16(i + 1)}
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
			if err := c.openClientLane(ctx, cfg, cancel, lane, mp); err != nil {
				errCh <- fmt.Errorf("lane %d: %w", lane.id+1, err)
			}
		}()
	}
	wg.Wait()
	close(errCh)

	ready := pool.readyCount()
	if ready < mp.MinReady {
		closeClientLanes(pool, "closed")
		var first error
		for err := range errCh {
			if first == nil {
				first = err
			}
			logger.Warnf("client multipath lane setup failed: %v", err)
		}
		if first == nil {
			first = fmt.Errorf("ready lanes below minimum: ready=%d min=%d", ready, mp.MinReady)
		}
		return fmt.Errorf("multipath setup failed: %w", first)
	}

	c.lanePool = pool
	logger.Infof("client multipath ready lanes=%d/%d control_lanes=%d min_ready=%d max_streams_per_lane=%d",
		ready, mp.Lanes, mp.ControlLanes, mp.MinReady, mp.MaxStreamsPerLane)
	return nil
}

func (c *Client) openClientLane(
	ctx context.Context,
	cfg Config,
	cancel context.CancelFunc,
	lane *clientLane,
	mp runtime.MultipathConfig,
) error {
	ln, err := transport.New(ctx, cfg.Transport, transport.Config{
		Carrier:   cfg.Carrier,
		RoomURL:   cfg.RoomURL,
		Engine:    cfg.Engine,
		URL:       cfg.URL,
		Token:     cfg.Token,
		ChannelID: cfg.ChannelID,
		DeviceID:  fmt.Sprintf("%s-lane-%02d", c.deviceID, lane.id+1),
		Name:      fmt.Sprintf("%s-%02d", names.Generate(), lane.id+1),
		OnData:    lane.onData,
		DNSServer: cfg.DNSServer,
		Options:   clientLaneOptions(cfg.TransportOptions, lane.protocolID),
		Traffic:   cfg.Traffic,
	})
	if err != nil {
		return fmt.Errorf("failed to create link: %w", err)
	}

	lane.ln = ln
	ln.SetEndedCallback(func(reason string) {
		lane.ready.Store(false)
		logger.Infof("Client multipath lane=%d reported conference end: %s", lane.id+1, reason)
		if lane.id < mp.ControlLanes {
			cancel()
		}
	})
	ln.SetShouldReconnect(func() bool { return ctx.Err() == nil })
	ln.SetReconnectCallback(func() {
		if ctx.Err() != nil {
			return
		}
		go func() {
			if !c.reconnectClientLane(ctx, cfg, lane, cancel, "carrier") && lane.id < mp.ControlLanes {
				cancel()
			}
		}()
	})

	if err := ln.Connect(ctx); err != nil {
		_ = ln.Close()
		return fmt.Errorf("failed to connect link: %w", err)
	}

	conn := muxconn.New(ln, c.cipher)
	sess, err := smux.Client(conn, smuxConfig(linkMaxPayload(ln)))
	if err != nil {
		_ = conn.Close()
		_ = ln.Close()
		return fmt.Errorf("smux client: %w", err)
	}
	controlStream, sid, err := openControlStream(ctx, sess, laneDeviceID(c.deviceID, lane), laneClaims(c.claims, lane, mp))
	if err != nil {
		_ = sess.Close()
		_ = conn.Close()
		_ = ln.Close()
		return fmt.Errorf("handshake: %w", err)
	}

	lane.mu.Lock()
	lane.conn = conn
	lane.session = sess
	lane.controlStrm = controlStream
	lane.sessionID = sid
	lane.ready.Store(true)
	lane.mu.Unlock()

	c.recordSession(sid)
	logger.Infof("session %s opened (device=%s lane=%d/%d)", sid, laneDeviceID(c.deviceID, lane), lane.id+1, mp.Lanes)
	c.startLaneControlLoop(ctx, cfg, lane, cancel)

	go ln.WatchConnection(ctx)
	return nil
}

func laneDeviceID(base string, lane *clientLane) string {
	return fmt.Sprintf("%s-lane-%02d", base, lane.id+1)
}

func laneClaims(base map[string]any, lane *clientLane, mp runtime.MultipathConfig) map[string]any {
	claims := make(map[string]any, len(base)+5)
	for k, v := range base {
		claims[k] = v
	}
	claims["multipath"] = true
	claims["multipath_lane"] = lane.id + 1
	claims["multipath_lanes"] = mp.Lanes
	claims["multipath_control_lane"] = lane.id < mp.ControlLanes
	return claims
}

func clientLaneOptions(base transport.Options, protocolID uint16) transport.Options {
	if opts, ok := base.(seichannel.Options); ok {
		opts.LaneID = protocolID
		return opts
	}
	return seichannel.Options{LaneID: protocolID}
}

func (c *Client) startLaneControlLoop(
	ctx context.Context,
	cfg Config,
	lane *clientLane,
	cancel context.CancelFunc,
) {
	lane.mu.RLock()
	stream := lane.controlStrm
	lane.mu.RUnlock()
	if stream == nil {
		return
	}

	controlCtx, stop := context.WithCancel(ctx)
	lane.mu.Lock()
	lane.controlStop = stop
	lane.mu.Unlock()

	liveness := cfg.Liveness
	onPong := liveness.OnPong
	onMissedPong := liveness.OnMissedPong
	onUnhealthy := liveness.OnUnhealthy
	liveness.OnPong = func(h control.Health) {
		lane.mu.RLock()
		sid := lane.sessionID
		lane.mu.RUnlock()
		c.recordPong(h)
		logger.Debugf("control alive session=%s lane=%d rtt=%v seq=%d", sid, lane.id+1, h.RTT, h.Seq)
		if onPong != nil {
			onPong(h)
		}
	}
	liveness.OnMissedPong = func(missed int) {
		c.recordMissed(missed)
		logger.Warnf("control missed pong on client lane=%d: missed_pongs=%d", lane.id+1, missed)
		if onMissedPong != nil {
			onMissedPong(missed)
		}
	}
	liveness.OnUnhealthy = func(missed int) {
		c.recordUnhealthy(missed)
		logger.Warnf("control stream unhealthy on client lane=%d: missed_pongs=%d", lane.id+1, missed)
		if onUnhealthy != nil {
			onUnhealthy(missed)
		}
	}

	go func() {
		err := control.Run(controlCtx, stream, liveness)
		if controlCtx.Err() != nil || ctx.Err() != nil {
			return
		}
		lane.ready.Store(false)
		if err != nil {
			logger.Warnf("client control stream ended lane=%d: %v", lane.id+1, err)
		}
		if !c.reconnectClientLane(ctx, cfg, lane, cancel, "liveness") && lane.id < cfg.Multipath.WithDefaults().ControlLanes {
			cancel()
		}
	}()
}

func (c *Client) reconnectClientLane(
	ctx context.Context,
	cfg Config,
	lane *clientLane,
	cancel context.CancelFunc,
	reason string,
) bool {
	c.reconnectMu.Lock()
	defer c.reconnectMu.Unlock()

	c.recordReconnect()
	lane.ready.Store(false)
	logger.Infof("client reconnect lane=%d reason=%s - tearing down smux session", lane.id+1, reason)
	resetLanePeer(lane)

	lane.mu.Lock()
	oldControl := lane.controlStrm
	oldControlStop := lane.controlStop
	oldSess := lane.session
	oldConn := lane.conn
	lane.session = nil
	lane.controlStrm = nil
	lane.controlStop = nil
	lane.sessionID = ""
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

	const (
		maxAttempts  = 5
		attemptDelay = 300 * time.Millisecond
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		logger.Infof("client reconnect lane=%d attempt=%d reason=%s", lane.id+1, attempt, reason)
		if c.tryReopenLaneSession(ctx, cfg, lane, cancel, attempt) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(attemptDelay):
		}
	}
	logger.Warnf("client reconnect lane=%d: exhausted %d handshake attempts", lane.id+1, maxAttempts)
	return false
}

func resetLanePeer(lane *clientLane) {
	lane.mu.RLock()
	ln := lane.ln
	lane.mu.RUnlock()
	if resetter, ok := ln.(interface{ ResetPeer() }); ok {
		resetter.ResetPeer()
	}
}

func (c *Client) tryReopenLaneSession(
	ctx context.Context,
	cfg Config,
	lane *clientLane,
	cancel context.CancelFunc,
	attempt int,
) bool {
	lane.mu.RLock()
	ln := lane.ln
	lane.mu.RUnlock()
	if ln == nil {
		return false
	}

	conn := muxconn.New(ln, c.cipher)
	sess, err := smux.Client(conn, smuxConfig(linkMaxPayload(ln)))
	if err != nil {
		logger.Warnf("smux re-init failed lane=%d (attempt %d): %v", lane.id+1, attempt, err)
		_ = conn.Close()
		return false
	}

	controlStream, sid, err := openControlStreamTimeout(
		ctx,
		sess,
		laneDeviceID(c.deviceID, lane),
		laneClaims(c.claims, lane, cfg.Multipath.WithDefaults()),
		handshake.DefaultTimeout,
	)
	if err != nil {
		logger.Warnf("handshake on reconnect failed lane=%d (attempt %d): %v", lane.id+1, attempt, err)
		_ = sess.Close()
		_ = conn.Close()
		return false
	}

	lane.mu.Lock()
	old := lane.conn
	lane.conn = conn
	lane.session = sess
	lane.controlStrm = controlStream
	lane.sessionID = sid
	lane.ready.Store(true)
	lane.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	c.recordSession(sid)
	logger.Infof("session %s reopened (device=%s lane=%d)", sid, laneDeviceID(c.deviceID, lane), lane.id+1)
	c.startLaneControlLoop(ctx, cfg, lane, cancel)
	return true
}

func (p *clientLanePool) readyCount() int {
	if p == nil {
		return 0
	}
	ready := 0
	for _, lane := range p.lanes {
		if lane != nil && lane.ready.Load() {
			ready++
		}
	}
	return ready
}

func (c *Client) pickLane() *clientLane {
	pool := c.lanePool
	if pool == nil {
		return nil
	}

	start := pool.cfg.ControlLanes
	if start >= len(pool.lanes) {
		start = 0
	}
	if lane := pickReadyLane(pool.lanes[start:], pool.cfg.MaxStreamsPerLane); lane != nil {
		return lane
	}
	if lane := pickReadyLane(pool.lanes[start:], 0); lane != nil {
		return lane
	}
	return pickReadyLane(pool.lanes[:start], 0)
}

func pickReadyLane(lanes []*clientLane, maxStreams int) *clientLane {
	var best *clientLane
	bestActive := int32(math.MaxInt32)
	for _, lane := range lanes {
		if lane == nil || !lane.ready.Load() {
			continue
		}
		active := lane.active.Load()
		if maxStreams > 0 && int(active) >= maxStreams {
			continue
		}
		if best == nil || active < bestActive {
			best = lane
			bestActive = active
		}
	}
	return best
}

func (c *Client) handleSocks5Multipath(
	_ context.Context,
	conn net.Conn,
	targetAddr string,
	targetPort int,
) {
	lane := c.pickLane()
	if lane == nil {
		_, _ = conn.Write(replyHostUnreachable())
		return
	}
	lane.active.Add(1)
	defer lane.active.Add(-1)

	lane.mu.RLock()
	sess := lane.session
	laneID := lane.id + 1
	lane.mu.RUnlock()
	if sess == nil || sess.IsClosed() {
		lane.ready.Store(false)
		_, _ = conn.Write(replyHostUnreachable())
		return
	}

	logger.Debugf("multipath selected lane=%d target=%s:%d active=%d",
		laneID, targetAddr, targetPort, lane.active.Load())
	c.tunnel(conn, sess, targetAddr, targetPort)
}

func (c *Client) shutdownMultipath() {
	pool := c.lanePool
	c.lanePool = nil
	closeClientLanes(pool, "closed")
}

func closeClientLanes(pool *clientLanePool, _ string) {
	if pool == nil {
		return
	}
	for _, lane := range pool.lanes {
		if lane == nil {
			continue
		}
		lane.ready.Store(false)
		lane.mu.Lock()
		controlStream := lane.controlStrm
		controlStop := lane.controlStop
		sess := lane.session
		conn := lane.conn
		ln := lane.ln
		lane.controlStrm = nil
		lane.controlStop = nil
		lane.session = nil
		lane.conn = nil
		lane.mu.Unlock()

		notifyControlClose(controlStream)
		if controlStop != nil {
			controlStop()
		}
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
	}
}
