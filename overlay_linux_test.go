//go:build linux

package main

import (
	"os"
	"testing"
	"time"
)

// TestWaylandOverlayLive drives the layer-shell overlay through its full
// lifecycle. It only runs under a live Wayland session (skipped otherwise);
// exercise it headless with:
//
//	WLR_BACKENDS=headless WLR_LIBINPUT_NO_DEVICES=1 cage -s -- go test -run TestWaylandOverlayLive -v
func TestWaylandOverlayLive(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("WAYLAND_DISPLAY not set; no live Wayland session")
	}
	ov, err := newWaylandOverlay()
	if err != nil {
		t.Fatalf("newWaylandOverlay: %v", err)
	}

	ov.Show()
	vals := make([]float64, waveformBars)
	for i := range vals {
		vals[i] = float64(i) / waveformBars
	}
	for i := 0; i < 10; i++ {
		ov.UpdateWaveform(vals, true)
		time.Sleep(20 * time.Millisecond)
	}

	ov.SetShiftHeld(true)
	ov.SetTranscribing(true)
	time.Sleep(200 * time.Millisecond)
	ov.SetTranscribing(false)
	ov.SetShiftHeld(false)

	ov.SetReplay("2/3  14:32:05 · 3.4s · “hello…”")
	time.Sleep(100 * time.Millisecond)
	ov.SetReplay("")

	ov.Hide()
	time.Sleep(100 * time.Millisecond)
}
