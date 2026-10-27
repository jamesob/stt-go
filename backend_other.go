//go:build !darwin && !linux

package main

import (
	"fmt"
	"runtime"
)

func newPlatformBackend() (*Backend, error) {
	return nil, fmt.Errorf("sttg supports macOS and Linux (GOOS=%s is not supported)", runtime.GOOS)
}

// runMainLoop blocks forever; on macOS it runs the Cocoa main loop instead.
func runMainLoop() {
	select {}
}

// platformInputDevices lists audio input devices for the setup wizard; not
// available on unsupported platforms.
func platformInputDevices() (names []string, defaultName string) {
	return nil, ""
}

func hotkeyName(id string) string {
	if name, ok := hotkeyDisplayNames[id]; ok {
		return name
	}
	return id
}
