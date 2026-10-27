//go:build darwin

package main

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <stdint.h>
#include <ApplicationServices/ApplicationServices.h>

extern int goKeyEvent(int t, int64_t keycode, uint64_t flags);

static CFMachPortRef stt_tap = NULL;
static CFRunLoopRef stt_tap_runloop = NULL;
static volatile int stt_tap_status = 0; // 0=starting, 1=running, -1=failed

static CGEventRef stt_tap_callback(CGEventTapProxy proxy, CGEventType type,
                                   CGEventRef event, void *refcon) {
	if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
		if (stt_tap != NULL) {
			CGEventTapEnable(stt_tap, true);
		}
		return event;
	}
	int t = -1;
	if (type == kCGEventKeyDown) {
		t = 0;
	} else if (type == kCGEventKeyUp) {
		t = 1;
	} else if (type == kCGEventFlagsChanged) {
		t = 2;
	}
	if (t >= 0) {
		int64_t keycode = CGEventGetIntegerValueField(event, kCGKeyboardEventKeycode);
		uint64_t flags = (uint64_t)CGEventGetFlags(event);
		if (goKeyEvent(t, keycode, flags)) {
			return NULL; // consume the event (replay picker keys)
		}
	}
	return event;
}

// stt_eventtap_run creates an event tap on the calling thread and services it
// until stt_eventtap_stop. The tap is active (not listen-only) so the replay
// picker can swallow arrow keys; everything else passes through untouched.
// Returns -1 if the tap could not be created (missing permissions).
static int stt_eventtap_run(void) {
	CGEventMask mask = CGEventMaskBit(kCGEventKeyDown) |
	                   CGEventMaskBit(kCGEventKeyUp) |
	                   CGEventMaskBit(kCGEventFlagsChanged);
	stt_tap_status = 0;
	stt_tap = CGEventTapCreate(kCGHIDEventTap, kCGHeadInsertEventTap,
	                           kCGEventTapOptionDefault, mask,
	                           stt_tap_callback, NULL);
	if (stt_tap == NULL) {
		stt_tap_status = -1;
		return -1;
	}
	CFRunLoopSourceRef source = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, stt_tap, 0);
	stt_tap_runloop = CFRunLoopGetCurrent();
	CFRunLoopAddSource(stt_tap_runloop, source, kCFRunLoopCommonModes);
	CGEventTapEnable(stt_tap, true);
	stt_tap_status = 1;
	CFRunLoopRun();
	CGEventTapEnable(stt_tap, false);
	CFRunLoopRemoveSource(stt_tap_runloop, source, kCFRunLoopCommonModes);
	CFRelease(source);
	CFRelease(stt_tap);
	stt_tap = NULL;
	return 0;
}

static int stt_eventtap_status(void) {
	return stt_tap_status;
}

static void stt_eventtap_stop(void) {
	if (stt_tap_runloop != NULL) {
		CFRunLoopStop(stt_tap_runloop);
	}
}

static int stt_ax_trusted(int prompt) {
	const void *keys[] = { kAXTrustedCheckOptionPrompt };
	const void *values[] = { prompt ? kCFBooleanTrue : kCFBooleanFalse };
	CFDictionaryRef options = CFDictionaryCreate(kCFAllocatorDefault, keys, values, 1,
	                                             &kCFCopyStringDictionaryKeyCallBacks,
	                                             &kCFTypeDictionaryValueCallBacks);
	Boolean trusted = AXIsProcessTrustedWithOptions(options);
	if (options != NULL) {
		CFRelease(options);
	}
	return trusted ? 1 : 0;
}

static uint64_t stt_hid_flags(void) {
	return (uint64_t)CGEventSourceFlagsState(kCGEventSourceStateHIDSystemState);
}
*/
import "C"

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// macOS virtual keycodes.
const (
	vkEscape = 53
	vkShiftL = 56
	vkShiftR = 60
	vkUp     = 126
	vkDown   = 125
)

// Device-dependent modifier flag bits (NX_DEVICE* from IOKit's IOLLEvent.h)
// distinguish left/right keys; class masks are the public kCGEventFlagMask*.
type hotkeySpec struct {
	keycode    int64
	deviceMask uint64
	classMask  uint64
}

var hotkeySpecs = map[string]hotkeySpec{
	"cmd_l":   {55, 0x00000008, 1 << 20},
	"cmd_r":   {54, 0x00000010, 1 << 20},
	"shift_l": {vkShiftL, 0x00000002, 1 << 17},
	"shift_r": {vkShiftR, 0x00000004, 1 << 17},
	"alt_l":   {58, 0x00000020, 1 << 19},
	"alt_r":   {61, 0x00000040, 1 << 19},
	"ctrl_l":  {59, 0x00000001, 1 << 18},
	"ctrl_r":  {62, 0x00002000, 1 << 18},
}

const maskShiftAny = 0x00000002 | 0x00000004

// darwinListener delivers global key events via a CGEventTap running on a
// dedicated OS thread. Designed for a single Start per process.
type darwinListener struct {
	events chan<- KeyEvent

	mu             sync.Mutex
	trigger        hotkeySpec
	triggerIsShift bool

	triggerDown atomic.Bool
}

// activeListener is consulted by the goKeyEvent cgo export, which has no
// per-instance context.
var activeListener atomic.Pointer[darwinListener]

func (l *darwinListener) Start(hotkeyID string, events chan<- KeyEvent) error {
	if err := l.SetHotkey(hotkeyID); err != nil {
		return err
	}
	l.events = events
	activeListener.Store(l)

	go func() {
		runtime.LockOSThread()
		C.stt_eventtap_run()
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		switch C.stt_eventtap_status() {
		case 1:
			return nil
		case -1:
			activeListener.Store(nil)
			return errors.New("could not create event tap (missing Accessibility/Input Monitoring permission?)")
		}
		time.Sleep(10 * time.Millisecond)
	}
	activeListener.Store(nil)
	return errors.New("event tap did not start in time")
}

func (l *darwinListener) Stop() {
	activeListener.Store(nil)
	C.stt_eventtap_stop()
}

func (l *darwinListener) SetHotkey(hotkeyID string) error {
	spec, ok := hotkeySpecs[hotkeyID]
	if !ok {
		return errors.New("unknown hotkey: " + hotkeyID)
	}
	l.mu.Lock()
	l.trigger = spec
	l.triggerIsShift = spec.keycode == vkShiftL || spec.keycode == vkShiftR
	l.mu.Unlock()
	l.triggerDown.Store(false)
	return nil
}

func (l *darwinListener) HotkeyHeld() bool {
	l.mu.Lock()
	mask := l.trigger.classMask
	l.mu.Unlock()
	return uint64(C.stt_hid_flags())&mask != 0
}

// handleKeyEvent runs on the event tap thread; it must stay fast and never
// block (send drops events when the channel is full). The return value tells
// the tap to consume the event instead of passing it to the focused app.
func (l *darwinListener) handleKeyEvent(typ int, keycode int64, flags uint64) bool {
	l.mu.Lock()
	trigger := l.trigger
	triggerIsShift := l.triggerIsShift
	l.mu.Unlock()

	switch typ {
	case 0: // key down
		if keycode == vkEscape {
			l.send(EvEsc)
			return false
		}
		if (keycode == vkUp || keycode == vkDown) && l.triggerDown.Load() {
			if keycode == vkUp {
				l.send(EvReplayNext)
			} else {
				l.send(EvReplayPrev)
			}
			return true // swallow: this is a replay-picker key
		}
	case 1: // key up
		if (keycode == vkUp || keycode == vkDown) && l.triggerDown.Load() {
			return true // swallow the matching key-up too
		}
	case 2: // flags changed
		if keycode == trigger.keycode {
			pressed := flags&trigger.deviceMask != 0
			l.triggerDown.Store(pressed)
			if pressed {
				l.send(EvHotkeyDown)
			} else {
				l.send(EvHotkeyUp)
			}
			return false
		}
		if (keycode == vkShiftL || keycode == vkShiftR) && !triggerIsShift {
			if flags&maskShiftAny != 0 {
				l.send(EvShiftDown)
			} else {
				l.send(EvShiftUp)
			}
		}
	}
	return false
}

func (l *darwinListener) send(ev KeyEvent) {
	select {
	case l.events <- ev:
	default:
	}
}

func axTrusted(prompt bool) bool {
	p := C.int(0)
	if prompt {
		p = 1
	}
	return C.stt_ax_trusted(p) != 0
}
