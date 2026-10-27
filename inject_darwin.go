//go:build darwin

package main

import (
	"fmt"
	"time"
)

const (
	pasteScript = `tell application "System Events" to keystroke "v" using command down`
	enterScript = `tell application "System Events" to key code 36`
)

// injectText pastes text into the frontmost app via the clipboard (Cmd+V
// through System Events), optionally follows with Enter, then restores the
// previous clipboard. Mirrors the behavior of the original Python version.
func injectText(text string, sendEnter bool) error {
	if text == "" {
		return nil
	}

	oldClipboard, _ := runOut(2*time.Second, "pbpaste")

	if err := runIn(2*time.Second, []byte(text), "pbcopy"); err != nil {
		return fmt.Errorf("pbcopy: %w", err)
	}
	defer func() {
		_ = runIn(2*time.Second, oldClipboard, "pbcopy")
	}()

	if err := run(5*time.Second, "osascript", "-e", pasteScript); err != nil {
		return fmt.Errorf("paste: %w", err)
	}
	if sendEnter {
		if err := run(5*time.Second, "osascript", "-e", enterScript); err != nil {
			return fmt.Errorf("enter: %w", err)
		}
	}
	// Give the target app a moment to read the clipboard before restoring it.
	time.Sleep(50 * time.Millisecond)
	return nil
}
