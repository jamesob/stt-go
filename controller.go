package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// appControl is the subset of App the controller drives (mockable in tests).
type appControl interface {
	StartRecording()
	ProcessRecording(sendEnter bool)
	CancelRecording()
	CancelTranscription()
	AbortRecording()
	ReplayRecording(path string, sendEnter bool)
	IsRecording() bool
	IsStarting() bool
	IsProcessing() bool
}

// Controller translates global key events into app actions:
// hold hotkey -> record, release -> process, Shift -> trailing Enter,
// Esc -> cancel, Up/Down while held -> pick a recent recording to replay.
// A poller recovers releases the event tap missed.
type Controller struct {
	app      appControl
	listener HotkeyListener
	overlay  Overlay
	store    *RecentStore

	mu             sync.Mutex
	hotkeyID       string
	triggerIsShift bool
	keyPressed     bool
	shiftHeld      bool
	sendEnter      bool
	lastForced     time.Time

	// Replay picker state (entered with Up while the hotkey is held).
	replayActive  bool
	replayEntries []RecentEntry
	replaySel     int

	events chan KeyEvent
	done   chan struct{}
}

func newController(app appControl, listener HotkeyListener, overlay Overlay, store *RecentStore, hotkeyID string) *Controller {
	if overlay == nil {
		overlay = nullOverlay{}
	}
	return &Controller{
		app:            app,
		listener:       listener,
		overlay:        overlay,
		store:          store,
		hotkeyID:       hotkeyID,
		triggerIsShift: strings.HasPrefix(hotkeyID, "shift"),
		events:         make(chan KeyEvent, 64),
		done:           make(chan struct{}),
	}
}

func (c *Controller) Start() error {
	if err := c.listener.Start(c.hotkeyID, c.events); err != nil {
		return err
	}
	go c.run()
	return nil
}

func (c *Controller) Stop() {
	c.listener.Stop()
	close(c.done)
}

// SetHotkey switches the trigger key at runtime (config reload).
func (c *Controller) SetHotkey(id string) error {
	if _, ok := hotkeyDisplayNames[id]; !ok {
		return fmt.Errorf("unknown hotkey %q", id)
	}
	if err := c.listener.SetHotkey(id); err != nil {
		return err
	}
	c.mu.Lock()
	c.hotkeyID = id
	c.triggerIsShift = strings.HasPrefix(id, "shift")
	c.mu.Unlock()
	return nil
}

func (c *Controller) run() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case ev := <-c.events:
			c.handle(ev)
		case <-ticker.C:
			c.releaseFallback()
		}
	}
}

func (c *Controller) handle(ev KeyEvent) {
	switch ev {
	case EvHotkeyDown:
		c.onHotkeyDown()
	case EvHotkeyUp:
		c.onHotkeyUp()
	case EvShiftDown:
		c.onShiftDown()
	case EvShiftUp:
		c.onShiftUp()
	case EvEsc:
		c.onEsc()
	case EvReplayNext:
		c.onReplayCycle(1)
	case EvReplayPrev:
		c.onReplayCycle(-1)
	}
}

func (c *Controller) onHotkeyDown() {
	c.mu.Lock()
	// Recover from a missed release: pressed flag set but nothing running.
	if c.keyPressed && !c.replayActive && !c.app.IsRecording() && !c.app.IsStarting() {
		c.keyPressed = false
		c.sendEnter = false
	}
	if c.keyPressed {
		c.mu.Unlock()
		return
	}
	c.keyPressed = true
	c.sendEnter = c.shiftHeld && !c.triggerIsShift
	sendEnter := c.sendEnter
	c.mu.Unlock()

	if sendEnter {
		c.overlay.SetShiftHeld(true)
	}
	go c.app.StartRecording()
}

func (c *Controller) onHotkeyUp() {
	c.mu.Lock()
	if !c.keyPressed {
		c.mu.Unlock()
		return
	}
	c.keyPressed = false
	sendEnter := c.sendEnter
	c.sendEnter = false

	if c.replayActive {
		c.replayActive = false
		var entry RecentEntry
		valid := len(c.replayEntries) > 0
		if valid {
			entry = c.replayEntries[c.replaySel]
		}
		c.replayEntries = nil
		c.mu.Unlock()
		if valid {
			go c.app.ReplayRecording(entry.Path, sendEnter)
		} else {
			c.overlay.Hide()
		}
		return
	}
	c.mu.Unlock()

	go c.app.ProcessRecording(sendEnter)
}

func (c *Controller) onShiftDown() {
	c.mu.Lock()
	c.shiftHeld = true
	becameEnter := false
	if c.app.IsRecording() && !c.sendEnter {
		c.sendEnter = true
		becameEnter = true
	}
	c.mu.Unlock()
	if becameEnter {
		c.overlay.SetShiftHeld(true)
	}
}

func (c *Controller) onShiftUp() {
	c.mu.Lock()
	c.shiftHeld = false
	c.mu.Unlock()
}

func (c *Controller) onEsc() {
	c.mu.Lock()
	if c.replayActive {
		c.replayActive = false
		c.replayEntries = nil
		c.keyPressed = false
		c.sendEnter = false
		c.mu.Unlock()
		info("Replay cancelled")
		c.overlay.Hide()
		return
	}
	c.mu.Unlock()

	if c.app.IsRecording() {
		c.mu.Lock()
		c.keyPressed = false
		c.sendEnter = false
		c.mu.Unlock()
		go c.app.CancelRecording()
	} else {
		go c.app.CancelTranscription()
	}
}

// onReplayCycle handles Up/Down while the hotkey is held: the first Up aborts
// the in-progress recording and opens the replay picker; further presses
// cycle through the recent recordings. Releasing the hotkey replays the
// selected one.
func (c *Controller) onReplayCycle(dir int) {
	c.mu.Lock()
	if !c.keyPressed {
		c.mu.Unlock()
		return
	}

	if !c.replayActive {
		if c.store == nil {
			c.mu.Unlock()
			return
		}
		entries := c.store.List()
		if len(entries) == 0 {
			c.mu.Unlock()
			warn("No recent recordings to replay")
			return
		}
		c.replayActive = true
		c.replayEntries = entries
		c.replaySel = 0
		entry := entries[0]
		total := len(entries)
		c.mu.Unlock()

		c.app.AbortRecording()
		c.overlay.Show()
		c.overlay.SetReplay(replayLabel(entry, 0, total))
		return
	}

	n := len(c.replayEntries)
	if n == 0 {
		c.replayActive = false
		c.mu.Unlock()
		return
	}
	c.replaySel = ((c.replaySel+dir)%n + n) % n
	entry := c.replayEntries[c.replaySel]
	sel := c.replaySel
	c.mu.Unlock()

	c.overlay.SetReplay(replayLabel(entry, sel, n))
}

// releaseFallback catches hotkey releases the event tap missed (e.g. during
// secure input): if we think the key is down but the OS says the modifier is
// no longer held, treat it as a release.
func (c *Controller) releaseFallback() {
	if !c.app.IsRecording() || c.app.IsStarting() || c.app.IsProcessing() {
		return
	}
	c.mu.Lock()
	if !c.keyPressed || c.replayActive || c.listener.HotkeyHeld() {
		c.mu.Unlock()
		return
	}
	if time.Since(c.lastForced) < time.Second {
		c.mu.Unlock()
		return
	}
	c.lastForced = time.Now()
	c.keyPressed = false
	sendEnter := c.sendEnter
	c.sendEnter = false
	c.mu.Unlock()

	debugf("release fallback fired")
	go c.app.ProcessRecording(sendEnter)
}
