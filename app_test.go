package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRecorder struct {
	mu        gatedMutex
	samples   []int16
	startErr  error
	active    bool
	starts    int
	stops     int
	cancels   int
	gotDevice string
}

// gatedMutex is just a sync.Mutex; named to avoid confusion with App.mu.
type gatedMutex = sync.Mutex

func (f *fakeRecorder) Start(device string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.gotDevice = device
	if f.startErr != nil {
		return f.startErr
	}
	f.active = true
	return nil
}

func (f *fakeRecorder) Stop() ([]int16, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	if !f.active {
		return nil, nil
	}
	f.active = false
	return f.samples, nil
}

func (f *fakeRecorder) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
	f.active = false
}

type fakeTranscriber struct {
	mu        sync.Mutex
	text      string
	err       error
	block     chan struct{} // when non-nil, wait for close or ctx cancel
	calls     int
	gotWav    []byte
	gotLang   string
	gotPrompt string
}

func (f *fakeTranscriber) Transcribe(ctx context.Context, wav []byte, language, prompt string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.gotWav = wav
	f.gotLang = language
	f.gotPrompt = prompt
	block := f.block
	text, err := f.text, f.err
	f.mu.Unlock()

	if block != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-block:
		}
	}
	return text, err
}

func (f *fakeTranscriber) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type injection struct {
	text  string
	enter bool
}

type fakeInjector struct {
	mu    sync.Mutex
	calls []injection
}

func (f *fakeInjector) inject(text string, enter bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, injection{text, enter})
	return nil
}

func (f *fakeInjector) all() []injection {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]injection(nil), f.calls...)
}

// fakeOverlay records overlay calls.
type fakeOverlay struct {
	mu           sync.Mutex
	shows        int
	hides        int
	transcribing []bool
	replays      []string
	shiftHeld    []bool
	updates      int
}

func (f *fakeOverlay) Show() { f.mu.Lock(); f.shows++; f.mu.Unlock() }
func (f *fakeOverlay) Hide() { f.mu.Lock(); f.hides++; f.mu.Unlock() }
func (f *fakeOverlay) UpdateWaveform([]float64, bool) {
	f.mu.Lock()
	f.updates++
	f.mu.Unlock()
}
func (f *fakeOverlay) SetTranscribing(on bool) {
	f.mu.Lock()
	f.transcribing = append(f.transcribing, on)
	f.mu.Unlock()
}
func (f *fakeOverlay) SetShiftHeld(held bool) {
	f.mu.Lock()
	f.shiftHeld = append(f.shiftHeld, held)
	f.mu.Unlock()
}
func (f *fakeOverlay) SetReplay(text string) {
	f.mu.Lock()
	f.replays = append(f.replays, text)
	f.mu.Unlock()
}

func testConfig(t *testing.T) *Config {
	return &Config{
		BaseURL:            "http://localhost:0/v1",
		Model:              "whisper-1",
		Language:           "en",
		Prompt:             "Claude, Anthropic",
		Hotkey:             "cmd_r",
		AudioDevice:        "Test Mic",
		SoundEnabled:       false,
		KeepRecordings:     false,
		RecordingsDir:      t.TempDir(),
		RecordingsMaxBytes: 1 << 30,
		RecentDir:          t.TempDir(),
		TimeoutS:           5,
	}
}

// loudSamples returns n samples well above the silence threshold.
func loudSamples(n int) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = 2000 // peak 2000/32768 ≈ 0.06
	}
	return s
}

func TestProcessRecordingHappyPath(t *testing.T) {
	rec := &fakeRecorder{samples: loudSamples(sampleRate)} // 1s of audio
	tr := &fakeTranscriber{text: " hello world "}
	inj := &fakeInjector{}
	app := newApp(testConfig(t), rec, tr, inj.inject, nil)

	app.StartRecording()
	if !app.IsRecording() {
		t.Fatal("expected recording after StartRecording")
	}
	if rec.gotDevice != "Test Mic" {
		t.Fatalf("device not passed to recorder: %q", rec.gotDevice)
	}
	app.ProcessRecording(false)

	if got := tr.callCount(); got != 1 {
		t.Fatalf("transcriber calls = %d, want 1", got)
	}
	if tr.gotLang != "en" || tr.gotPrompt != "Claude, Anthropic" {
		t.Fatalf("language/prompt not passed through: %q %q", tr.gotLang, tr.gotPrompt)
	}
	wantWavLen := 44 + sampleRate*2
	if len(tr.gotWav) != wantWavLen {
		t.Fatalf("wav length = %d, want %d", len(tr.gotWav), wantWavLen)
	}
	calls := inj.all()
	if len(calls) != 1 || calls[0].text != "hello world" || calls[0].enter {
		t.Fatalf("unexpected injections: %+v", calls)
	}
	if app.IsRecording() || app.IsProcessing() {
		t.Fatal("app should be idle after processing")
	}
}

func TestOverlayLifecycle(t *testing.T) {
	rec := &fakeRecorder{samples: loudSamples(sampleRate)}
	tr := &fakeTranscriber{text: "hi"}
	ov := &fakeOverlay{}
	app := newApp(testConfig(t), rec, tr, nil, nil)
	app.overlay = ov

	app.StartRecording()
	app.ProcessRecording(false)

	ov.mu.Lock()
	defer ov.mu.Unlock()
	if ov.shows != 1 {
		t.Fatalf("overlay shows = %d, want 1", ov.shows)
	}
	if ov.hides == 0 {
		t.Fatal("overlay should be hidden after processing")
	}
	// SetTranscribing(true) on stop, then false at the end.
	if len(ov.transcribing) < 2 || !ov.transcribing[0] || ov.transcribing[len(ov.transcribing)-1] {
		t.Fatalf("transcribing sequence wrong: %v", ov.transcribing)
	}
}

func TestProcessRecordingSendEnter(t *testing.T) {
	rec := &fakeRecorder{samples: loudSamples(sampleRate)}
	tr := &fakeTranscriber{text: "ship it"}
	inj := &fakeInjector{}
	app := newApp(testConfig(t), rec, tr, inj.inject, nil)

	app.StartRecording()
	app.ProcessRecording(true)

	calls := inj.all()
	if len(calls) != 1 || !calls[0].enter {
		t.Fatalf("expected one injection with enter, got %+v", calls)
	}
}

func TestSlashTransformApplied(t *testing.T) {
	rec := &fakeRecorder{samples: loudSamples(sampleRate)}
	tr := &fakeTranscriber{text: "Slash help me"}
	inj := &fakeInjector{}
	app := newApp(testConfig(t), rec, tr, inj.inject, nil)

	app.StartRecording()
	app.ProcessRecording(false)

	calls := inj.all()
	if len(calls) != 1 || calls[0].text != "/help me" {
		t.Fatalf("slash transform not applied: %+v", calls)
	}
}

func TestTooShortSkipped(t *testing.T) {
	rec := &fakeRecorder{samples: loudSamples(sampleRate / 4)} // 0.25s
	tr := &fakeTranscriber{text: "nope"}
	inj := &fakeInjector{}
	app := newApp(testConfig(t), rec, tr, inj.inject, nil)

	app.StartRecording()
	app.ProcessRecording(false)

	if tr.callCount() != 0 {
		t.Fatal("transcriber should not be called for a too-short recording")
	}
	if len(inj.all()) != 0 {
		t.Fatal("nothing should be typed for a too-short recording")
	}
}

func TestSilenceSkipped(t *testing.T) {
	quiet := make([]int16, sampleRate)
	for i := range quiet {
		quiet[i] = 100 // peak 100/32768 ≈ 0.003 < 0.01
	}
	rec := &fakeRecorder{samples: quiet}
	tr := &fakeTranscriber{text: "nope"}
	app := newApp(testConfig(t), rec, tr, nil, nil)

	app.StartRecording()
	app.ProcessRecording(false)

	if tr.callCount() != 0 {
		t.Fatal("transcriber should not be called for silence")
	}
}

func TestTranscribeRetriesThenGivesUp(t *testing.T) {
	rec := &fakeRecorder{samples: loudSamples(sampleRate)}
	tr := &fakeTranscriber{err: errors.New("boom")}
	inj := &fakeInjector{}
	app := newApp(testConfig(t), rec, tr, inj.inject, nil)

	app.StartRecording()
	app.ProcessRecording(false)

	if got := tr.callCount(); got != transcribeTries {
		t.Fatalf("transcriber calls = %d, want %d", got, transcribeTries)
	}
	if len(inj.all()) != 0 {
		t.Fatal("nothing should be typed when transcription fails")
	}
}

func TestCancelTranscription(t *testing.T) {
	rec := &fakeRecorder{samples: loudSamples(sampleRate)}
	tr := &fakeTranscriber{text: "late result", block: make(chan struct{})}
	inj := &fakeInjector{}
	app := newApp(testConfig(t), rec, tr, inj.inject, nil)

	app.StartRecording()
	done := make(chan struct{})
	go func() {
		app.ProcessRecording(false)
		close(done)
	}()

	eventually(t, func() bool { return tr.callCount() == 1 })
	app.CancelTranscription()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ProcessRecording did not return after cancel")
	}
	if len(inj.all()) != 0 {
		t.Fatal("nothing should be typed after cancellation")
	}
	if app.IsProcessing() {
		t.Fatal("app should not be processing after cancel")
	}
}

func TestCancelRecording(t *testing.T) {
	rec := &fakeRecorder{samples: loudSamples(sampleRate)}
	tr := &fakeTranscriber{text: "x"}
	app := newApp(testConfig(t), rec, tr, nil, nil)

	app.StartRecording()
	app.CancelRecording()

	if app.IsRecording() {
		t.Fatal("should not be recording after cancel")
	}
	rec.mu.Lock()
	cancels := rec.cancels
	rec.mu.Unlock()
	if cancels == 0 {
		t.Fatal("recorder.Cancel should have been called")
	}
	// A process after cancel should find nothing to do.
	app.ProcessRecording(false)
	if tr.callCount() != 0 {
		t.Fatal("transcriber should not run after a cancelled recording")
	}
}

func TestAbortRecordingIsSilent(t *testing.T) {
	rec := &fakeRecorder{samples: loudSamples(sampleRate)}
	ov := &fakeOverlay{}
	var sounds []SoundName
	var soundMu sync.Mutex
	cfg := testConfig(t)
	cfg.SoundEnabled = true
	app := newApp(cfg, rec, &fakeTranscriber{}, nil, func(s SoundName) {
		soundMu.Lock()
		sounds = append(sounds, s)
		soundMu.Unlock()
	})
	app.overlay = ov

	app.StartRecording()
	app.AbortRecording()

	if app.IsRecording() {
		t.Fatal("should not be recording after abort")
	}
	soundMu.Lock()
	defer soundMu.Unlock()
	for _, s := range sounds {
		if s == SoundCancel || s == SoundStop {
			t.Fatal("abort must not play stop/cancel sounds")
		}
	}
	ov.mu.Lock()
	defer ov.mu.Unlock()
	if ov.hides != 0 {
		t.Fatal("abort must keep the overlay visible for the replay picker")
	}
}

func TestReplayRecording(t *testing.T) {
	cfg := testConfig(t)
	store := newRecentStore(cfg.RecentDir, recentMax)
	wav := encodeWAV(loudSamples(sampleRate), sampleRate)
	path := store.Save(wav)
	if path == "" {
		t.Fatal("save failed")
	}

	tr := &fakeTranscriber{text: "replayed words"}
	inj := &fakeInjector{}
	app := newApp(cfg, &fakeRecorder{}, tr, inj.inject, nil)
	app.recent = store

	app.ReplayRecording(path, true)

	if tr.callCount() != 1 {
		t.Fatalf("transcriber calls = %d, want 1", tr.callCount())
	}
	if len(tr.gotWav) != len(wav) {
		t.Fatalf("wav bytes = %d, want %d", len(tr.gotWav), len(wav))
	}
	calls := inj.all()
	if len(calls) != 1 || calls[0].text != "replayed words" || !calls[0].enter {
		t.Fatalf("unexpected injections: %+v", calls)
	}
	// Transcript sidecar should now exist for the picker.
	entries := store.List()
	if len(entries) != 1 || entries[0].Transcript != "replayed words" {
		t.Fatalf("transcript not stored: %+v", entries)
	}
}

func TestProcessRecordingSavesToRecentStore(t *testing.T) {
	cfg := testConfig(t)
	store := newRecentStore(cfg.RecentDir, recentMax)
	rec := &fakeRecorder{samples: loudSamples(sampleRate)}
	tr := &fakeTranscriber{text: "kept words"}
	app := newApp(cfg, rec, tr, nil, nil)
	app.recent = store

	app.StartRecording()
	app.ProcessRecording(false)

	entries := store.List()
	if len(entries) != 1 {
		t.Fatalf("recent entries = %d, want 1", len(entries))
	}
	if entries[0].Transcript != "kept words" {
		t.Fatalf("transcript = %q", entries[0].Transcript)
	}
	if entries[0].Seconds < 0.9 || entries[0].Seconds > 1.1 {
		t.Fatalf("duration = %f, want ~1s", entries[0].Seconds)
	}
}

func TestRecentSavedEvenWhenTranscriptionFails(t *testing.T) {
	cfg := testConfig(t)
	store := newRecentStore(cfg.RecentDir, recentMax)
	rec := &fakeRecorder{samples: loudSamples(sampleRate)}
	tr := &fakeTranscriber{err: errors.New("server down")}
	app := newApp(cfg, rec, tr, nil, nil)
	app.recent = store

	app.StartRecording()
	app.ProcessRecording(false)

	// The whole point: the WAV survives a crashed server, for replay.
	entries := store.List()
	if len(entries) != 1 {
		t.Fatalf("recent entries = %d, want 1 (wav must be kept on failure)", len(entries))
	}
	if entries[0].Transcript != "" {
		t.Fatalf("no transcript expected, got %q", entries[0].Transcript)
	}
}

func TestStartIgnoredWhileRecording(t *testing.T) {
	rec := &fakeRecorder{samples: loudSamples(sampleRate)}
	app := newApp(testConfig(t), rec, &fakeTranscriber{}, nil, nil)

	app.StartRecording()
	app.StartRecording() // should be ignored

	rec.mu.Lock()
	starts := rec.starts
	rec.mu.Unlock()
	if starts != 1 {
		t.Fatalf("recorder starts = %d, want 1", starts)
	}
}

func TestStartRecorderFailure(t *testing.T) {
	rec := &fakeRecorder{startErr: errors.New("no device")}
	app := newApp(testConfig(t), rec, &fakeTranscriber{}, nil, nil)

	app.StartRecording()
	if app.IsRecording() || app.IsStarting() {
		t.Fatal("app should be idle after recorder start failure")
	}
}

func TestKeepRecordingsArchives(t *testing.T) {
	cfg := testConfig(t)
	cfg.KeepRecordings = true
	rec := &fakeRecorder{samples: loudSamples(sampleRate)}
	tr := &fakeTranscriber{text: "archived words"}
	app := newApp(cfg, rec, tr, nil, nil)

	app.StartRecording()
	app.ProcessRecording(false)

	entries, err := os.ReadDir(cfg.RecordingsDir)
	if err != nil {
		t.Fatal(err)
	}
	var wavs, txts int
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case ".wav":
			wavs++
		case ".txt":
			txts++
			data, _ := os.ReadFile(filepath.Join(cfg.RecordingsDir, e.Name()))
			if !strings.Contains(string(data), "archived words") {
				t.Fatalf("transcript content wrong: %q", data)
			}
		}
	}
	if wavs != 1 || txts != 1 {
		t.Fatalf("archive contents: %d wav, %d txt; want 1 and 1", wavs, txts)
	}
}

func TestSetConfigLiveReload(t *testing.T) {
	rec := &fakeRecorder{samples: loudSamples(sampleRate)}
	tr := &fakeTranscriber{text: "x"}
	app := newApp(testConfig(t), rec, tr, nil, nil)

	newCfg := testConfig(t)
	newCfg.Language = "de"
	newCfg.AudioDevice = "Other Mic"
	app.SetConfig(newCfg)

	app.StartRecording()
	app.ProcessRecording(false)

	if tr.gotLang != "de" {
		t.Fatalf("language after reload = %q, want de", tr.gotLang)
	}
	if rec.gotDevice != "Other Mic" {
		t.Fatalf("device after reload = %q, want Other Mic", rec.gotDevice)
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
