package seichannel

import "sync"

// fragAckTracker tracks acknowledgements for individual SEI fragments.
//
// SEI frames travel over a lossy video path: one missing fragment should not
// force the sender to retransmit the whole message. The sender registers the
// fragment count for a sequence number, the receive loop marks every decoded
// fragment, and Send retries only what is still pending.
type fragAckTracker struct {
	mu      sync.Mutex
	pending map[uint32]*fragWaiter
}

type fragWaiter struct {
	mu        sync.Mutex
	crc       uint32
	total     int
	acked     []bool
	remaining int
	notify    chan struct{}
}

func newFragAckTracker() *fragAckTracker {
	return &fragAckTracker{pending: make(map[uint32]*fragWaiter)}
}

func (t *fragAckTracker) Register(seq, crc uint32, total int) *fragWaiter {
	w := &fragWaiter{
		crc:       crc,
		total:     total,
		acked:     make([]bool, total),
		remaining: total,
		notify:    make(chan struct{}, 1),
	}
	t.mu.Lock()
	t.pending[seq] = w
	t.mu.Unlock()
	return w
}

func (t *fragAckTracker) Unregister(seq uint32) {
	t.mu.Lock()
	delete(t.pending, seq)
	t.mu.Unlock()
}

func (t *fragAckTracker) Mark(seq, crc uint32, fragIdx int) bool {
	t.mu.Lock()
	w, ok := t.pending[seq]
	t.mu.Unlock()
	if !ok {
		return false
	}

	w.mu.Lock()
	if w.crc != crc || fragIdx < 0 || fragIdx >= w.total || w.acked[fragIdx] {
		w.mu.Unlock()
		return false
	}
	w.acked[fragIdx] = true
	w.remaining--
	w.mu.Unlock()

	select {
	case w.notify <- struct{}{}:
	default:
	}
	return true
}

func (w *fragWaiter) Pending() []int {
	w.mu.Lock()
	defer w.mu.Unlock()

	out := make([]int, 0, w.remaining)
	for idx, acked := range w.acked {
		if !acked {
			out = append(out, idx)
		}
	}
	return out
}

func (w *fragWaiter) Done() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.remaining == 0
}

func (w *fragWaiter) Notify() <-chan struct{} {
	return w.notify
}
