package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEncodeWAV(t *testing.T) {
	samples := []int16{0, 1000, -1000}
	wav := encodeWAV(samples, 16000)

	if len(wav) != 44+6 {
		t.Fatalf("len = %d, want 50", len(wav))
	}
	if !bytes.Equal(wav[0:4], []byte("RIFF")) || !bytes.Equal(wav[8:12], []byte("WAVE")) {
		t.Fatal("bad RIFF/WAVE header")
	}
	if got := binary.LittleEndian.Uint32(wav[4:8]); got != 36+6 {
		t.Fatalf("riff size = %d", got)
	}
	if got := binary.LittleEndian.Uint16(wav[20:22]); got != 1 {
		t.Fatalf("format = %d, want 1 (PCM)", got)
	}
	if got := binary.LittleEndian.Uint16(wav[22:24]); got != 1 {
		t.Fatalf("channels = %d, want 1", got)
	}
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != 16000 {
		t.Fatalf("sample rate = %d", got)
	}
	if got := binary.LittleEndian.Uint32(wav[28:32]); got != 32000 {
		t.Fatalf("byte rate = %d", got)
	}
	if got := binary.LittleEndian.Uint16(wav[34:36]); got != 16 {
		t.Fatalf("bits = %d", got)
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != 6 {
		t.Fatalf("data size = %d", got)
	}
	if got := int16(binary.LittleEndian.Uint16(wav[46:48])); got != 1000 {
		t.Fatalf("sample[1] = %d", got)
	}
	if got := int16(binary.LittleEndian.Uint16(wav[48:50])); got != -1000 {
		t.Fatalf("sample[2] = %d", got)
	}
}

func TestPeakOf(t *testing.T) {
	if p := peakOf(nil); p != 0 {
		t.Fatalf("peak of empty = %f", p)
	}
	if p := peakOf([]int16{0, 100, -32768}); p != 1.0 {
		t.Fatalf("peak = %f, want 1.0", p)
	}
	p := peakOf([]int16{327}) // ≈ 0.00998 — just below the threshold
	if p >= silenceThreshold {
		t.Fatalf("peak = %f, should be below threshold", p)
	}
	p = peakOf([]int16{330}) // ≈ 0.0100 — just above
	if p < silenceThreshold {
		t.Fatalf("peak = %f, should be above threshold", p)
	}
}
