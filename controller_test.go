package main

import (
	"sync"
	"testing"
	"time"
)

type replayCall struct {
	path      string
	sendEnter bool
}

type mockApp struct {
	mu         sync.Mutex
	recording  bool
	processing bool
	starts     int
	processes  []bool // sendEnter values
	recCancels int
	trxCancels int
	aborts     int
	replays    []replayCall
}

func (m *mockApp) StartRecording() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts++
	m.recording = true
}

func (m *mockApp) ProcessRecording(sendEnter bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processes = append(m.processes, sendEnter)
	m.recording = false
}

func (m *mockApp) CancelRecording() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recCancels++
	m.recording = false
}

func (m *mockApp) CancelTranscription() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trxCancels++
}

func (m *mockApp) AbortRecording() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aborts++
	m.recording = false
}

func (m *mockApp) ReplayRecording(path string, sendEnter bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replays = append(m.replays, replayCall{path, sendEnter})
}

func (m *mockApp) IsRecording() bool  { m.mu.Lock(); defer m.mu.Unlock(); return m.recording }
func (m *mockApp) IsStarting() bool   { return false }
func (m *mockApp) IsProcessing() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.processing }

func (m *mockApp) processCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.processes)
}

func (m *mockApp) startCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.starts
}

func (m *mockApp) abortCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.aborts
}

func (m *mockApp) replayCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.replays)
}

type fakeListener struct {
	mu      sync.Mutex
	held    bool
	started bool
	hotkey  string
}

func (f *fakeListener) Start(hotkeyID string, events chan<- KeyEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	f.hotkey = hotkeyID
	return nil
}
func (f *fakeListener) Stop() {}
func (f *fakeListener) SetHotkey(hotkeyID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hotkey = hotkeyID
	return nil
}
func (f *fakeListener) HotkeyHeld() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.held
}

func emptyStore(t *testing.T) *RecentStore {
	return newRecentStore(t.TempDir(), recentMax)
}

func newTestController(t *testing.T, app appControl) *Controller {
	return newController(app, &fakeListener{held: true}, nullOverlay{}, emptyStore(t), "cmd_r")
}

func TestHoldAndRelease(t *testing.T) {
	app := &mockApp{}
	c := newTestController(t, app)

	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })

	c.handle(EvHotkeyUp)
	eventually(t, func() bool { return app.processCount() == 1 })

	app.mu.Lock()
	defer app.mu.Unlock()
	if app.processes[0] {
		t.Fatal("sendEnter should be false without shift")
	}
}

func TestShiftDuringRecordingSendsEnter(t *testing.T) {
	app := &mockApp{}
	c := newTestController(t, app)

	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })
	c.handle(EvShiftDown)
	c.handle(EvHotkeyUp)
	eventually(t, func() bool { return app.processCount() == 1 })

	app.mu.Lock()
	defer app.mu.Unlock()
	if !app.processes[0] {
		t.Fatal("sendEnter should be true when shift pressed during recording")
	}
}

func TestShiftHeldBeforePressSendsEnter(t *testing.T) {
	app := &mockApp{}
	c := newTestController(t, app)

	c.handle(EvShiftDown)
	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })
	c.handle(EvHotkeyUp)
	eventually(t, func() bool { return app.processCount() == 1 })

	app.mu.Lock()
	defer app.mu.Unlock()
	if !app.processes[0] {
		t.Fatal("sendEnter should be true when shift held before press")
	}
}

func TestShiftIgnoredWhenTriggerIsShift(t *testing.T) {
	app := &mockApp{}
	c := newController(app, &fakeListener{held: true}, nullOverlay{}, emptyStore(t), "shift_r")

	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })
	c.handle(EvHotkeyUp)
	eventually(t, func() bool { return app.processCount() == 1 })

	app.mu.Lock()
	defer app.mu.Unlock()
	if app.processes[0] {
		t.Fatal("sendEnter should never be set by the trigger itself")
	}
}

func TestReleaseWithoutPressIgnored(t *testing.T) {
	app := &mockApp{}
	c := newTestController(t, app)

	c.handle(EvHotkeyUp)
	if app.processCount() != 0 {
		t.Fatal("release without press should be ignored")
	}
}

func TestEscCancelsRecording(t *testing.T) {
	app := &mockApp{}
	c := newTestController(t, app)

	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })
	c.handle(EvEsc)
	eventually(t, func() bool {
		app.mu.Lock()
		defer app.mu.Unlock()
		return app.recCancels == 1
	})

	// Releasing the key afterwards must not process anything.
	c.handle(EvHotkeyUp)
	if app.processCount() != 0 {
		t.Fatal("release after esc should be ignored")
	}
}

func TestEscCancelsTranscriptionWhenNotRecording(t *testing.T) {
	app := &mockApp{}
	c := newTestController(t, app)

	c.handle(EvEsc)
	eventually(t, func() bool {
		app.mu.Lock()
		defer app.mu.Unlock()
		return app.trxCancels == 1
	})
}

func TestReleaseFallback(t *testing.T) {
	app := &mockApp{}
	lis := &fakeListener{held: false} // OS says key is not held
	c := newController(app, lis, nullOverlay{}, emptyStore(t), "cmd_r")

	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })

	// Simulate the periodic poll: key flagged as pressed, OS disagrees.
	c.releaseFallback()
	eventually(t, func() bool { return app.processCount() == 1 })
}

func TestReleaseFallbackRateLimited(t *testing.T) {
	app := &mockApp{}
	lis := &fakeListener{held: false}
	c := newController(app, lis, nullOverlay{}, emptyStore(t), "cmd_r")

	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })
	c.releaseFallback()
	eventually(t, func() bool { return app.processCount() == 1 })

	// Immediately after, a second press + fallback within 1s must not fire.
	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 2 })
	c.releaseFallback()
	if app.processCount() != 1 {
		t.Fatal("fallback should be rate-limited to once per second")
	}
}

// --- Replay picker ---

// storeWithEntries creates a RecentStore holding two recordings and returns
// the paths, oldest first.
func storeWithEntries(t *testing.T) (*RecentStore, []string) {
	t.Helper()
	store := newRecentStore(t.TempDir(), recentMax)
	var paths []string
	for i := 0; i < 2; i++ {
		p := store.Save(encodeWAV(loudSamples(sampleRate/2), sampleRate))
		if p == "" {
			t.Fatal("save failed")
		}
		paths = append(paths, p)
		time.Sleep(5 * time.Millisecond) // distinct millisecond timestamps
	}
	return store, paths
}

func TestReplayPickAndRelease(t *testing.T) {
	store, paths := storeWithEntries(t)
	app := &mockApp{}
	ov := &fakeOverlay{}
	c := newController(app, &fakeListener{held: true}, ov, store, "cmd_r")

	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })

	// First Up: abort recording, open picker on newest.
	c.handle(EvReplayNext)
	eventually(t, func() bool { return app.abortCount() == 1 })
	ov.mu.Lock()
	nReplays := len(ov.replays)
	ov.mu.Unlock()
	if nReplays != 1 {
		t.Fatalf("overlay replay labels = %d, want 1", nReplays)
	}

	// Second Up: cycle to the older entry.
	c.handle(EvReplayNext)
	// Release: replay the selected (older) one.
	c.handle(EvHotkeyUp)
	eventually(t, func() bool { return app.replayCount() == 1 })

	app.mu.Lock()
	defer app.mu.Unlock()
	if app.replays[0].path != paths[0] {
		t.Fatalf("replayed %s, want oldest %s", app.replays[0].path, paths[0])
	}
	if len(app.processes) != 0 {
		t.Fatal("release in replay mode must not process a recording")
	}
}

func TestReplayCycleWrapsAround(t *testing.T) {
	store, paths := storeWithEntries(t)
	app := &mockApp{}
	c := newController(app, &fakeListener{held: true}, nullOverlay{}, store, "cmd_r")

	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })
	c.handle(EvReplayNext) // enter, sel=0 (newest)
	eventually(t, func() bool { return app.abortCount() == 1 })
	c.handle(EvReplayNext) // sel=1 (oldest)
	c.handle(EvReplayNext) // wraps to sel=0
	c.handle(EvHotkeyUp)
	eventually(t, func() bool { return app.replayCount() == 1 })

	app.mu.Lock()
	defer app.mu.Unlock()
	if app.replays[0].path != paths[1] {
		t.Fatalf("replayed %s, want newest %s", app.replays[0].path, paths[1])
	}
}

func TestReplayDownCyclesBackwards(t *testing.T) {
	store, paths := storeWithEntries(t)
	app := &mockApp{}
	c := newController(app, &fakeListener{held: true}, nullOverlay{}, store, "cmd_r")

	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })
	c.handle(EvReplayNext) // enter at sel=0
	eventually(t, func() bool { return app.abortCount() == 1 })
	c.handle(EvReplayPrev) // backwards wraps to sel=1 (oldest)
	c.handle(EvHotkeyUp)
	eventually(t, func() bool { return app.replayCount() == 1 })

	app.mu.Lock()
	defer app.mu.Unlock()
	if app.replays[0].path != paths[0] {
		t.Fatalf("replayed %s, want oldest %s", app.replays[0].path, paths[0])
	}
}

func TestReplayEscCancels(t *testing.T) {
	store, _ := storeWithEntries(t)
	app := &mockApp{}
	ov := &fakeOverlay{}
	c := newController(app, &fakeListener{held: true}, ov, store, "cmd_r")

	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })
	c.handle(EvReplayNext)
	eventually(t, func() bool { return app.abortCount() == 1 })
	c.handle(EvEsc)

	// Release after Esc: nothing replayed, nothing processed.
	c.handle(EvHotkeyUp)
	time.Sleep(50 * time.Millisecond)
	if app.replayCount() != 0 || app.processCount() != 0 {
		t.Fatal("esc should cancel replay mode entirely")
	}
	ov.mu.Lock()
	defer ov.mu.Unlock()
	if ov.hides == 0 {
		t.Fatal("overlay should hide when replay is cancelled")
	}
}

func TestReplayWithEmptyStoreIgnored(t *testing.T) {
	app := &mockApp{}
	c := newTestController(t, app) // empty store

	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })
	c.handle(EvReplayNext)
	time.Sleep(50 * time.Millisecond)
	if app.abortCount() != 0 {
		t.Fatal("empty store must not abort the recording")
	}
	// Recording continues; release processes normally.
	c.handle(EvHotkeyUp)
	eventually(t, func() bool { return app.processCount() == 1 })
}

func TestReplayIgnoredWithoutHotkeyHeld(t *testing.T) {
	store, _ := storeWithEntries(t)
	app := &mockApp{}
	c := newController(app, &fakeListener{held: true}, nullOverlay{}, store, "cmd_r")

	c.handle(EvReplayNext)
	time.Sleep(50 * time.Millisecond)
	if app.abortCount() != 0 || app.replayCount() != 0 {
		t.Fatal("Up without the hotkey held must be ignored")
	}
}

func TestSetHotkey(t *testing.T) {
	app := &mockApp{}
	lis := &fakeListener{held: true}
	c := newController(app, lis, nullOverlay{}, emptyStore(t), "cmd_r")

	if err := c.SetHotkey("shift_r"); err != nil {
		t.Fatal(err)
	}
	lis.mu.Lock()
	hk := lis.hotkey
	lis.mu.Unlock()
	if hk != "shift_r" {
		t.Fatalf("listener hotkey = %q", hk)
	}
	// With a shift trigger, pressing it must not set sendEnter.
	c.handle(EvHotkeyDown)
	eventually(t, func() bool { return app.startCount() == 1 })
	c.handle(EvHotkeyUp)
	eventually(t, func() bool { return app.processCount() == 1 })
	app.mu.Lock()
	defer app.mu.Unlock()
	if app.processes[0] {
		t.Fatal("sendEnter must be false for a shift trigger")
	}

	if err := c.SetHotkey("bogus"); err == nil {
		t.Fatal("bogus hotkey should error")
	}
}
