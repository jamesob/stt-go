//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Linux input-event key codes (linux/input-event-codes.h).
const (
	keyEsc        = 1
	keyEnter      = 28
	keySpace      = 57
	keyA          = 30
	keyZ          = 44
	keyLeftShift  = 42
	keyRightShift = 54
	keyLeftCtrl   = 29
	keyRightCtrl  = 97
	keyLeftAlt    = 56
	keyRightAlt   = 100
	keyLeftMeta   = 125 // Left Super
	keyRightMeta  = 126 // Right Super
	keyUp         = 103
	keyDown       = 108
)

const evKey = 1 // EV_KEY

var evdevHotkeyCodes = map[string]uint16{
	"cmd_l":   keyLeftMeta,
	"cmd_r":   keyRightMeta,
	"alt_l":   keyLeftAlt,
	"alt_r":   keyRightAlt,
	"ctrl_l":  keyLeftCtrl,
	"ctrl_r":  keyRightCtrl,
	"shift_l": keyLeftShift,
	"shift_r": keyRightShift,
}

// evdev ioctl numbers: _IOC(_IOC_READ, 'E', nr, size).
const (
	eviocgbitEVKey = uintptr((2 << 30) | (128 << 16) | (0x45 << 8) | (0x20 + evKey))
	eviocgname     = uintptr((2 << 30) | (256 << 16) | (0x45 << 8) | 0x06)
)

var evdevDeviceBlacklist = []string{"yubikey", "fido", "u2f", "security key", "smartcard"}

// evdevEventSize is sizeof(struct input_event): two time words plus
// u16/u16/u32. 24 bytes on 64-bit kernels, 16 on 32-bit.
func evdevEventSize() int {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return 24
	}
	return 16
}

func evdevBitSet(bits []byte, code uint16) bool {
	i, b := int(code)/8, code%8
	return i < len(bits) && bits[i]&(1<<b) != 0
}

// evdevListener watches /dev/input/event* keyboards directly (works on both
// Wayland and X11), mirroring the Python version's evdev listener.
type evdevListener struct {
	events chan<- KeyEvent

	mu             sync.Mutex
	trigger        uint16
	triggerIsShift bool

	triggerDown atomic.Bool

	stop    chan struct{}
	wakeR   *os.File
	wakeW   *os.File
	devs    map[string]*os.File
	wg      sync.WaitGroup
	started bool
}

func (l *evdevListener) SetHotkey(hotkeyID string) error {
	code, ok := evdevHotkeyCodes[hotkeyID]
	if !ok {
		return errors.New("unknown hotkey: " + hotkeyID)
	}
	l.mu.Lock()
	l.trigger = code
	l.triggerIsShift = code == keyLeftShift || code == keyRightShift
	l.mu.Unlock()
	l.triggerDown.Store(false)
	return nil
}

func (l *evdevListener) Start(hotkeyID string, events chan<- KeyEvent) error {
	if err := l.SetHotkey(hotkeyID); err != nil {
		return err
	}
	l.events = events

	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	l.wakeR, l.wakeW = r, w
	l.stop = make(chan struct{})
	l.devs = map[string]*os.File{}

	// Fail fast with actionable advice when no keyboard can be read at all
	// (usually the user is not in the 'input' group).
	l.scan()
	if len(l.devs) == 0 {
		r.Close()
		w.Close()
		return fmt.Errorf("no readable keyboards under /dev/input — add yourself to the input group with: sudo usermod -aG input $USER, then log out and back in")
	}

	l.started = true
	l.wg.Add(1)
	go l.run()
	return nil
}

func (l *evdevListener) Stop() {
	if !l.started {
		return
	}
	l.started = false
	close(l.stop)
	_, _ = l.wakeW.Write([]byte{0})
	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	for path, f := range l.devs {
		f.Close()
		delete(l.devs, path)
	}
	l.wakeR.Close()
	l.wakeW.Close()
}

func (l *evdevListener) HotkeyHeld() bool { return l.triggerDown.Load() }

// isKeyboard applies the Python version's capability and blacklist checks.
func isKeyboard(f *os.File, name string) bool {
	bits := make([]byte, 128)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), eviocgbitEVKey, uintptr(unsafe.Pointer(&bits[0])))
	if errno != 0 {
		return false
	}
	for _, code := range []uint16{keyA, keyZ, keySpace, keyEnter} {
		if !evdevBitSet(bits, code) {
			return false
		}
	}
	lower := strings.ToLower(name)
	for _, bad := range evdevDeviceBlacklist {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	return true
}

func openKeyboard(path string) (*os.File, string, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", false
	}
	nameBuf := make([]byte, 256)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), eviocgname, uintptr(unsafe.Pointer(&nameBuf[0])))
	name := ""
	if errno == 0 {
		if i := bytes.IndexByte(nameBuf, 0); i >= 0 {
			name = string(nameBuf[:i])
		}
	}
	if !isKeyboard(f, name) {
		f.Close()
		return nil, "", false
	}
	return f, name, true
}

// scan discovers new keyboards, adding any not yet monitored.
func (l *evdevListener) scan() {
	paths, _ := filepath.Glob("/dev/input/event*")
	for _, path := range paths {
		if _, ok := l.devs[path]; ok {
			continue
		}
		f, name, ok := openKeyboard(path)
		if !ok {
			continue
		}
		l.devs[path] = f
		info("evdev: monitoring %s (%s)", path, name)
	}
}

func (l *evdevListener) run() {
	defer l.wg.Done()
	lastScan := time.Now()
	buf := make([]byte, 4096)
	evSize := evdevEventSize()

	for {
		select {
		case <-l.stop:
			return
		default:
		}
		if time.Since(lastScan) >= 3*time.Second {
			l.scan()
			lastScan = time.Now()
		}

		var rfds syscall.FdSet
		maxFd := -1
		set := func(fd int) {
			rfds.Bits[fd/64] |= 1 << (uint(fd) % 64)
			if fd > maxFd {
				maxFd = fd
			}
		}
		for _, f := range l.devs {
			set(int(f.Fd()))
		}
		set(int(l.wakeR.Fd()))

		tv := syscall.NsecToTimeval(int64(500 * time.Millisecond))
		n, err := syscall.Select(maxFd+1, &rfds, nil, nil, &tv)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return
		}
		if n == 0 {
			continue
		}
		if fdsetHas(&rfds, int(l.wakeR.Fd())) {
			return
		}
		for path, f := range l.devs {
			if !fdsetHas(&rfds, int(f.Fd())) {
				continue
			}
			m, err := f.Read(buf)
			if err != nil {
				if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
					continue
				}
				// Device unplugged.
				debugf("evdev: lost device %s (%v)", path, err)
				f.Close()
				delete(l.devs, path)
				continue
			}
			for off := 0; off+evSize <= m; off += evSize {
				ev := buf[off:]
				var typ, code uint16
				var value int32
				if evSize == 24 {
					typ = uint16(ev[16]) | uint16(ev[17])<<8
					code = uint16(ev[18]) | uint16(ev[19])<<8
					value = int32(uint32(ev[20]) | uint32(ev[21])<<8 | uint32(ev[22])<<16 | uint32(ev[23])<<24)
				} else {
					typ = uint16(ev[8]) | uint16(ev[9])<<8
					code = uint16(ev[10]) | uint16(ev[11])<<8
					value = int32(uint32(ev[12]) | uint32(ev[13])<<8 | uint32(ev[14])<<16 | uint32(ev[15])<<24)
				}
				l.handle(typ, code, value)
			}
		}
	}
}

func fdsetHas(s *syscall.FdSet, fd int) bool {
	if fd < 0 {
		return false
	}
	return s.Bits[fd/64]&(1<<(uint(fd)%64)) != 0
}

// handle maps one key event onto the shared KeyEvent stream. Unlike the
// macOS event tap, evdev cannot swallow the replay-picker arrows; they also
// reach the focused app.
func (l *evdevListener) handle(typ, code uint16, value int32) {
	if typ != evKey {
		return
	}
	l.mu.Lock()
	trigger := l.trigger
	triggerIsShift := l.triggerIsShift
	l.mu.Unlock()

	if code == trigger {
		switch value {
		case 1: // a fresh physical press edge (autorepeat is separate)
			l.triggerDown.Store(true)
			l.send(EvHotkeyDown)
		case 0:
			if l.triggerDown.Swap(false) {
				l.send(EvHotkeyUp)
			}
		}
		return
	}

	if value == 1 { // presses only
		switch code {
		case keyEsc:
			l.send(EvEsc)
		case keyUp:
			if l.triggerDown.Load() {
				l.send(EvReplayNext)
			}
		case keyDown:
			if l.triggerDown.Load() {
				l.send(EvReplayPrev)
			}
		case keyLeftShift, keyRightShift:
			if !triggerIsShift {
				l.send(EvShiftDown)
			}
		}
		return
	}
	if value == 0 && !triggerIsShift && (code == keyLeftShift || code == keyRightShift) {
		l.send(EvShiftUp)
	}
}

func (l *evdevListener) send(ev KeyEvent) {
	select {
	case l.events <- ev:
	default:
	}
}
