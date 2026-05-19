package videochannel

import (
	"bytes"
	"testing"
)

func TestReadH264AccessUnitsPreservesFrameBoundaries(t *testing.T) {
	stream := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x01,
		0x00, 0x00, 0x01, 0x68, 0x02,
		0x00, 0x00, 0x00, 0x01, 0x65, 0x03,
		0x00, 0x00, 0x01, 0x67, 0x04,
		0x00, 0x00, 0x00, 0x01, 0x68, 0x05,
		0x00, 0x00, 0x01, 0x65, 0x06,
	}

	var samples [][]byte
	err := readH264AccessUnits(bytes.NewReader(stream), func(sample []byte) bool {
		samples = append(samples, sample)
		return true
	})
	if err != nil {
		t.Fatalf("readH264AccessUnits returned error: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 access units, got %d", len(samples))
	}
	for i, sample := range samples {
		if !bytes.Contains(sample, []byte{0x00, 0x00, 0x00, 0x01, 0x67}) {
			t.Fatalf("sample %d does not contain SPS with Annex-B prefix", i)
		}
		if !bytes.Contains(sample, []byte{0x00, 0x00, 0x00, 0x01, 0x65}) {
			t.Fatalf("sample %d does not contain IDR with Annex-B prefix", i)
		}
	}
}
