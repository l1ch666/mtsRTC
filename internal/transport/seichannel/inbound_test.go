package seichannel

import (
	"bytes"
	"hash/crc32"
	"testing"

	"github.com/openlibrecommunity/olcrtc/internal/transport/common"
)

func TestInboundAssemblyAndAck(t *testing.T) {
	var got []byte
	tr := &streamTransport{
		onData:      func(data []byte) { got = append([]byte(nil), data...) },
		outboundAck: make(chan []byte, 4),
		reassembler: common.NewReassembler(256),
	}

	payload := []byte("hello world")
	crc := crc32.ChecksumIEEE(payload)
	tr.handleInboundFrame(transportFrame{
		typ:       frameTypeData,
		seq:       1,
		crc:       crc,
		totalLen:  uint32(len(payload)), //nolint:gosec // G115: bounded conversion verified by surrounding logic
		fragIdx:   1,
		fragTotal: 2,
		payload:   []byte(" world"),
	})
	if len(got) != 0 {
		t.Fatalf("onData called before message complete: %q", got)
	}
	assertNextAck(t, tr.outboundAck, 1, crc, 1)

	tr.handleInboundFrame(transportFrame{
		typ:       frameTypeData,
		seq:       1,
		crc:       crc,
		totalLen:  uint32(len(payload)), //nolint:gosec // G115: bounded conversion verified by surrounding logic
		fragIdx:   0,
		fragTotal: 2,
		payload:   []byte("hello"),
	})
	if !bytes.Equal(got, payload) {
		t.Fatalf("assembled payload = %q, want %q", got, payload)
	}
	assertNextAck(t, tr.outboundAck, 1, crc, 0)

	got = nil
	tr.handleInboundFrame(transportFrame{
		typ:       frameTypeData,
		seq:       1,
		crc:       crc,
		totalLen:  uint32(len(payload)), //nolint:gosec // G115: bounded conversion verified by surrounding logic
		fragIdx:   0,
		fragTotal: 2,
		payload:   []byte("hello"),
	})
	if got != nil {
		t.Fatalf("duplicate delivered payload again: %q", got)
	}
	assertNextAck(t, tr.outboundAck, 1, crc, 0)
}

func TestInboundRejectsBadCRC(t *testing.T) {
	tr := &streamTransport{
		outboundAck: make(chan []byte, 2),
		reassembler: common.NewReassembler(256),
	}

	called := false
	tr.onData = func([]byte) { called = true }
	tr.handleInboundFrame(transportFrame{
		seq:       2,
		crc:       123,
		totalLen:  3,
		fragIdx:   0,
		fragTotal: 1,
		payload:   []byte("abc"),
	})
	if called {
		t.Fatal("handleInboundFrame() delivered payload with bad crc")
	}
}

func assertNextAck(t *testing.T, ch <-chan []byte, seq, crc uint32, fragIdx uint16) {
	t.Helper()
	select {
	case ack := <-ch:
		frame, err := decodeTransportFrame(ack)
		if err != nil || frame.typ != frameTypeAck || frame.seq != seq || frame.crc != crc || frame.fragIdx != fragIdx {
			t.Fatalf("ack frame = %+v err=%v, want seq=%d crc=%d frag=%d", frame, err, seq, crc, fragIdx)
		}
	default:
		t.Fatal("handleInboundFrame() did not enqueue ack")
	}
}
