//go:build darwin

package main

import "os/exec"

var soundPaths = map[SoundName]string{
	SoundStart:  "/System/Library/Sounds/Tink.aiff",
	SoundStop:   "/System/Library/Sounds/Pop.aiff",
	SoundCancel: "/System/Library/Sounds/Basso.aiff",
}

func playSound(name SoundName) {
	path, ok := soundPaths[name]
	if !ok {
		return
	}
	cmd := exec.Command("afplay", path)
	if err := cmd.Start(); err != nil {
		return
	}
	go cmd.Wait() // reap to avoid zombies
}
