//go:build linux

package main

import "os/exec"

// freedesktop.org sound icons, mapped from the macOS sounds the Python
// version uses on darwin.
var soundPaths = map[SoundName]string{
	SoundStart:  "/usr/share/sounds/freedesktop/stereo/message-new-instant.oga",
	SoundStop:   "/usr/share/sounds/freedesktop/stereo/message.oga",
	SoundCancel: "/usr/share/sounds/freedesktop/stereo/dialog-warning.oga",
}

func playSound(name SoundName) {
	path, ok := soundPaths[name]
	if !ok {
		return
	}
	for _, tool := range []string{"paplay", "aplay"} {
		if _, err := exec.LookPath(tool); err != nil {
			continue
		}
		cmd := exec.Command(tool, path)
		if err := cmd.Start(); err != nil {
			continue
		}
		go cmd.Wait() // reap to avoid zombies
		return
	}
}
