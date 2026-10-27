//go:build linux

package main

/*
#cgo pkg-config: wayland-client pangocairo
#cgo LDFLAGS: -lm
#include <stdlib.h>
#include "overlay_wayland_linux.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// waylandOverlay renders the recording pill through wlr-layer-shell, which
// sway and other wlroots compositors support. All methods are safe to call
// from any goroutine; they are no-ops if stt_wl_init failed.
type waylandOverlay struct{}

// newWaylandOverlay connects to the Wayland compositor. It returns an error
// (and a null overlay experience via the caller) when there is no usable
// Wayland session or the compositor lacks wlr-layer-shell / argb shm.
func newWaylandOverlay() (Overlay, error) {
	if C.stt_wl_init() != 0 {
		return nil, errors.New("wayland overlay unavailable (no compositor, wlr-layer-shell, or ARGB shm)")
	}
	return waylandOverlay{}, nil
}

func (waylandOverlay) Show() { C.stt_wl_show() }
func (waylandOverlay) Hide() { C.stt_wl_hide() }

func (waylandOverlay) UpdateWaveform(values []float64, aboveThreshold bool) {
	if len(values) == 0 {
		return
	}
	arr := make([]C.float, len(values))
	for i, v := range values {
		arr[i] = C.float(v)
	}
	above := C.int(0)
	if aboveThreshold {
		above = 1
	}
	C.stt_wl_update(&arr[0], C.int(len(arr)), above)
}

func (waylandOverlay) SetTranscribing(on bool) {
	v := C.int(0)
	if on {
		v = 1
	}
	C.stt_wl_set_transcribing(v)
}

func (waylandOverlay) SetShiftHeld(held bool) {
	v := C.int(0)
	if held {
		v = 1
	}
	C.stt_wl_set_shift(v)
}

func (waylandOverlay) SetReplay(text string) {
	cs := C.CString(text)
	defer C.free(unsafe.Pointer(cs))
	C.stt_wl_set_replay(cs)
}
