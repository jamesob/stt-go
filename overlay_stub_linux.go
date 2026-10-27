//go:build linux && !cgo

package main

import "errors"

// Fallback for CGO_ENABLED=0 builds: no Wayland overlay, nullOverlay is used.
func newWaylandOverlay() (Overlay, error) {
	return nil, errors.New("built without cgo; wayland overlay unavailable")
}
