//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// injectText types text into the focused window: wtype on Wayland (like the
// Python version), xdotool on X11. Clipboard-restore injection is not needed
// because the text is typed, not pasted.
func injectText(text string, sendEnter bool) error {
	if text == "" {
		return nil
	}
	// Prefer the tool matching the active session; fall back to whichever
	// one is installed.
	useWtype := lookupTool("wtype") && (os.Getenv("WAYLAND_DISPLAY") != "" || !lookupTool("xdotool"))
	if useWtype {
		if err := run(5*time.Second, "wtype", "--", text); err != nil {
			return fmt.Errorf("wtype: %w", err)
		}
		if sendEnter {
			if err := run(2*time.Second, "wtype", "-P", "Return", "-p", "Return"); err != nil {
				return fmt.Errorf("wtype enter: %w", err)
			}
		}
		return nil
	}
	if lookupTool("xdotool") {
		if err := run(5*time.Second, "xdotool", "type", "--clearmodifiers", text); err != nil {
			return fmt.Errorf("xdotool: %w", err)
		}
		if sendEnter {
			if err := run(2*time.Second, "xdotool", "key", "Return"); err != nil {
				return fmt.Errorf("xdotool enter: %w", err)
			}
		}
		return nil
	}
	return errors.New("no text injector found: install wtype (Wayland) or xdotool (X11)")
}
