//go:build linux

package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// linuxHotkeyNames mirrors the Python version's HOTKEY_DISPLAY_NAMES_LINUX.
var linuxHotkeyNames = map[string]string{
	"cmd_r":   "Right Super",
	"cmd_l":   "Left Super",
	"alt_r":   "Right Alt",
	"alt_l":   "Left Alt",
	"ctrl_r":  "Right Ctrl",
	"ctrl_l":  "Left Ctrl",
	"shift_r": "Right Shift",
	"shift_l": "Left Shift",
}

func hotkeyName(id string) string {
	if name, ok := linuxHotkeyNames[id]; ok {
		return name
	}
	if name, ok := hotkeyDisplayNames[id]; ok {
		return name
	}
	return id
}

// runMainLoop blocks forever; signal handling and the config watcher run on
// their own goroutines, so there is no windowing main loop to service.
func runMainLoop() {
	select {}
}

func newPlatformBackend() (*Backend, error) {
	ensureXdgRuntime()
	overlay := Overlay(nil)
	if ov, err := newWaylandOverlay(); err == nil {
		overlay = ov
	} else {
		debugf("%v", err)
	}
	return &Backend{
		Recorder:         &linuxRecorder{},
		Listener:         &evdevListener{},
		Overlay:          overlay, // nil under X11-only sessions (nullOverlay)
		Inject:           injectText,
		PlaySound:        playSound,
		CheckPermissions: checkLinuxDeps,
		PermissionHelp:   "",
		ListenerHelp: "sttg captures the hotkey from /dev/input via evdev.\n" +
			"Grant read access with:  sudo usermod -aG input $USER\n" +
			"then log out and back in. Verify with:  ls -l /dev/input/event*",
	}, nil
}

// ensureXdgRuntime points XDG_RUNTIME_DIR at the per-user runtime dir when a
// bare shell left it unset, so PipeWire / pactl keep working.
func ensureXdgRuntime() {
	if os.Getenv("XDG_RUNTIME_DIR") != "" {
		return
	}
	rt := fmt.Sprintf("/run/user/%d", os.Getuid())
	if fi, err := os.Stat(rt); err == nil && fi.IsDir() {
		_ = os.Setenv("XDG_RUNTIME_DIR", rt)
	}
}

// checkLinuxDeps warns about missing Linux dependencies, mirroring the Python
// version's check_linux_deps. Non-fatal: warnings only, so capture still works
// even without, say, a text injector.
func checkLinuxDeps() bool {
	var problems []string

	// Text injection.
	if !lookupTool("wtype") && !lookupTool("xdotool") {
		problems = append(problems,
			"neither 'wtype' (Wayland) nor 'xdotool' (X11) found — typed text will not appear.")
	}

	// Sound feedback (optional).
	if !lookupTool("paplay") && !lookupTool("aplay") {
		problems = append(problems,
			"neither 'paplay' nor 'aplay' found — sound feedback will be silent.")
	}

	// Audio capture.
	if !pipewireAvailable() && !lookupTool("arecord") {
		problems = append(problems,
			"no audio capture tool found — install pw-record (pipewire-utils) or arecord (alsa-utils).")
	}

	// Keyboard capture: input group membership (evdev reads /dev/input).
	if !inInputGroup() {
		problems = append(problems,
			"user not in 'input' group — keyboard capture will fail.\n"+
				"  Fix: sudo usermod -aG input $USER   then log out and back in.")
	}

	if len(problems) == 0 {
		return true
	}

	bar := strings.Repeat("=", 52)
	fmt.Println()
	fmt.Println(bar)
	fmt.Println("  MISSING LINUX DEPENDENCIES")
	fmt.Println(bar)
	fmt.Println()
	for _, p := range problems {
		fmt.Printf("  * %s\n", p)
	}
	fmt.Println()
	fmt.Println(bar)
	fmt.Println()
	return true
}

// inInputGroup reports whether the current user can read /dev/input devices,
// approximated by 'input' group membership (or root).
func inInputGroup() bool {
	if os.Geteuid() == 0 {
		return true
	}
	u, err := user.Current()
	if err != nil {
		return true // can't tell; don't block on it
	}
	groupIds, err := u.GroupIds()
	if err != nil {
		groupIds = nil
	}
	gids := append([]string{u.Gid}, groupIds...)
	if g, err := user.LookupGroup("input"); err == nil {
		for _, g2 := range gids {
			if g2 == g.Gid {
				return true
			}
		}
	}
	// The group may be named differently; fall back to a direct read probe.
	return probeInputReadable()
}

func probeInputReadable() bool {
	matches, _ := filepath.Glob("/dev/input/event*")
	for _, m := range matches {
		if f, err := os.Open(m); err == nil {
			f.Close()
			return true
		}
	}
	return false
}
