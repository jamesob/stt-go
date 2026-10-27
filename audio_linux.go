//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// linuxRecorder captures 16 kHz mono s16le PCM by streaming raw output from
// pw-record (PipeWire) or arecord (ALSA), the Linux counterparts of the
// Python version's sounddevice/PortAudio capture.
type linuxRecorder struct {
	mu        sync.Mutex
	active    bool
	samples   []int16
	wfSamples []int16 // samples since the last overlay poll

	cmd     *exec.Cmd
	readEnd *os.File
	ioDone  chan struct{} // closed when the stdout reader finishes
	waitErr chan error    // closed when the child exits
}

func pipewireAvailable() bool {
	if _, err := exec.LookPath("pw-record"); err != nil {
		return false
	}
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		// Common fallback (e.g. running under sudo or a bare shell).
		rt = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	_, err := os.Stat(filepath.Join(rt, "pipewire-0"))
	return err == nil
}

func defaultAlias(device string) bool {
	switch strings.ToLower(device) {
	case "", "default", "pipewire", "pulse", "pulseaudio":
		return true
	}
	return false
}

// pactlSources lists non-monitor source node names; nil when pactl is
// unusable (then device names are passed through unchecked).
func pactlSources() []string {
	out, err := exec.Command("pactl", "list", "short", "sources").Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[1]
		if name == "auto.monitor" || strings.HasSuffix(name, ".monitor") {
			continue
		}
		names = append(names, name)
	}
	return names
}

// recordingCmd builds the capture command, resolving the configured device
// against the live source list where possible.
func recordingCmd(device string) (*exec.Cmd, error) {
	switch {
	case pipewireAvailable():
		target := ""
		if !defaultAlias(device) {
			if sources := pactlSources(); sources != nil {
				found := false
				for _, s := range sources {
					if s == device {
						found = true
						break
					}
				}
				if !found {
					warn("Audio device %q not found; using system default", device)
				} else {
					target = device
				}
			} else {
				target = device
			}
		}
		args := []string{"--rate", strconv.Itoa(sampleRate), "--channels", "1", "--format", "s16", "--quality", "5"}
		if target != "" {
			args = append(args, "--target", target)
		}
		args = append(args, "-") // stdout
		return exec.Command("pw-record", args...), nil

	case lookupTool("arecord"):
		target := ""
		if !defaultAlias(device) {
			if strings.HasPrefix(device, "hw:") || strings.HasPrefix(device, "plughw:") {
				target = device
			} else {
				warn("Audio device %q not usable with ALSA; using system default", device)
			}
		}
		args := []string{"-q", "-t", "raw", "-f", "S16_LE", "-r", strconv.Itoa(sampleRate), "-c", "1"}
		if target != "" {
			args = append(args, "-D", target)
		}
		return exec.Command("arecord", args...), nil
	}
	return nil, errors.New("no audio recorder found (install pw-record from pipewire-utils or arecord from alsa-utils)")
}

func lookupTool(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (r *linuxRecorder) Start(deviceName string) error {
	r.mu.Lock()
	if r.active {
		r.mu.Unlock()
		return errors.New("already recording")
	}
	r.samples = nil
	r.wfSamples = nil
	r.active = true
	r.mu.Unlock()

	cmd, err := recordingCmd(deviceName)
	fail := func(err error) error {
		r.mu.Lock()
		r.active = false
		r.mu.Unlock()
		return err
	}
	if err != nil {
		return fail(err)
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		return fail(err)
	}
	cmd.Stdout = pw
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return fail(fmt.Errorf("start audio capture: %w", err))
	}
	pw.Close() // the child holds the only remaining write end

	r.cmd, r.readEnd = cmd, pr
	r.ioDone = make(chan struct{})
	r.waitErr = make(chan error, 1)

	go func() {
		defer close(r.ioDone)
		defer pr.Close()
		buf := make([]byte, 32768)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				r.append(pcm16le(buf[:n]))
			}
			if err != nil {
				return
			}
		}
	}()
	go func() { r.waitErr <- cmd.Wait() }()

	// Catch instant failures (device busy, node gone) so callers get an
	// error instead of an empty recording.
	select {
	case werr := <-r.waitErr:
		if werr != nil {
			return fail(fmt.Errorf("audio capture exited immediately: %w", werr))
		}
	case <-time.After(300 * time.Millisecond):
	}
	return nil
}

func (r *linuxRecorder) append(s []int16) {
	r.mu.Lock()
	if r.active {
		r.samples = append(r.samples, s...)
		r.wfSamples = append(r.wfSamples, s...)
	}
	r.mu.Unlock()
}

// TakeWaveformSamples implements WaveformSource for the overlay.
func (r *linuxRecorder) TakeWaveformSamples() []int16 {
	r.mu.Lock()
	s := r.wfSamples
	r.wfSamples = nil
	r.mu.Unlock()
	return s
}

func (r *linuxRecorder) Stop() ([]int16, error) {
	r.mu.Lock()
	if !r.active {
		r.mu.Unlock()
		return nil, nil
	}
	r.mu.Unlock()

	r.terminate()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = false
	s := r.samples
	r.samples = nil
	r.wfSamples = nil
	return s, nil
}

func (r *linuxRecorder) Cancel() {
	r.terminate()
	r.mu.Lock()
	r.active = false
	r.samples = nil
	r.wfSamples = nil
	r.mu.Unlock()
}

// terminate stops the child (SIGTERM, then SIGKILL) and waits for the stdout
// reader to drain.
func (r *linuxRecorder) terminate() {
	r.mu.Lock()
	cmd, ioDone, waitErr := r.cmd, r.ioDone, r.waitErr
	r.cmd, r.readEnd, r.ioDone, r.waitErr = nil, nil, nil, nil
	r.mu.Unlock()

	if cmd == nil {
		return
	}
	if cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-ioDone:
	case <-time.After(200 * time.Millisecond):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-ioDone:
		case <-time.After(500 * time.Millisecond):
		}
	}
	select {
	case <-waitErr:
	case <-time.After(500 * time.Millisecond):
	}
}

func pcm16le(b []byte) []int16 {
	n := len(b) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

// platformInputDevices lists capture sources for the setup wizard: PipeWire /
// PulseAudio source node names (what --target and the config use), falling
// back to ALSA card/device ids.
func platformInputDevices() (names []string, defaultName string) {
	if sources := pactlSources(); sources != nil {
		if out, err := exec.Command("pactl", "get-default-source").Output(); err == nil {
			defaultName = strings.TrimSpace(string(out))
		}
		return sources, defaultName
	}
	return alsaInputDevices()
}

func alsaInputDevices() (names []string, defaultName string) {
	out, err := exec.Command("arecord", "-l").Output()
	if err != nil {
		return nil, ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		// card 0: PCH [HDA Intel PCH], device 0: ALC269 Analog [ALC269 Analog]
		if !strings.HasPrefix(line, "card ") {
			continue
		}
		cardID := ""
		if i := strings.Index(line, ": "); i > 0 {
			rest := line[i+2:]
			if j := strings.Index(rest, " "); j > 0 {
				cardID = rest[:j]
			}
		}
		dev := "0"
		if i := strings.Index(line, ", device "); i > 0 {
			rest := line[i+len(", device "):]
			if j := strings.Index(rest, ":"); j > 0 {
				dev = strings.TrimSpace(rest[:j])
			}
		}
		if cardID != "" {
			names = append(names, fmt.Sprintf("plughw:%s,%s", cardID, dev))
		}
	}
	return names, ""
}
