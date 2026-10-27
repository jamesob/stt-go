package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	sampleRate       = 16000 // Whisper expects 16kHz
	silenceThreshold = 0.01  // skip transcription if peak below this
	minSeconds       = 0.5   // skip recordings shorter than this

	maxStartingTime = 5 * time.Second
	transcribeTries = 3 // 1 attempt + 2 retries
)

// App is the core state machine: record -> transcribe -> type.
// It is platform-independent; all side effects are injected.
type App struct {
	mu         sync.Mutex
	cfg        *Config
	recording  bool
	starting   bool
	processing bool
	opID       int
	cancelOp   context.CancelFunc // cancels an in-flight transcription

	waveformActive atomic.Bool

	recorder    Recorder
	transcriber Transcriber
	inject      func(text string, sendEnter bool) error
	sound       func(SoundName)
	overlay     Overlay
	recent      *RecentStore
}

func newApp(cfg *Config, recorder Recorder, transcriber Transcriber, inject func(string, bool) error, sound func(SoundName)) *App {
	if inject == nil {
		inject = func(string, bool) error { return nil }
	}
	if sound == nil {
		sound = func(SoundName) {}
	}
	return &App{
		cfg:         cfg,
		recorder:    recorder,
		transcriber: transcriber,
		inject:      inject,
		sound:       sound,
		overlay:     nullOverlay{},
	}
}

func (a *App) IsRecording() bool  { a.mu.Lock(); defer a.mu.Unlock(); return a.recording }
func (a *App) IsStarting() bool   { a.mu.Lock(); defer a.mu.Unlock(); return a.starting }
func (a *App) IsProcessing() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.processing }

// snap returns the current config snapshot (treated as immutable).
func (a *App) snap() *Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// SetConfig swaps in a new config (live reload).
func (a *App) SetConfig(cfg *Config) {
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
}

// SetTranscriber swaps the transcription client (live reload).
func (a *App) SetTranscriber(t Transcriber) {
	a.mu.Lock()
	a.transcriber = t
	a.mu.Unlock()
}

func (a *App) playSound(s SoundName) {
	if a.snap().SoundEnabled {
		a.sound(s)
	}
}

// StartRecording begins audio capture. Ignored while busy.
func (a *App) StartRecording() {
	a.mu.Lock()
	if a.processing {
		a.mu.Unlock()
		debugf("start ignored: processing")
		return
	}
	if a.recording || a.starting {
		a.mu.Unlock()
		debugf("start ignored: busy")
		return
	}
	a.starting = true
	a.recording = true
	a.mu.Unlock()

	a.overlay.Show()
	a.playSound(SoundStart)
	info("Recording...")

	err := a.startRecorderWithTimeout(a.snap().AudioDevice, maxStartingTime)
	if err != nil {
		errorf("Failed to start recording: %v", err)
		a.recorder.Cancel()
		a.mu.Lock()
		a.recording = false
		a.starting = false
		a.mu.Unlock()
		a.overlay.Hide()
		return
	}

	go a.waveformLoop()

	// If the hotkey was released (or Esc pressed) while the recorder was
	// starting, the recording flag is already false: discard the capture.
	a.mu.Lock()
	stillRecording := a.recording
	a.mu.Unlock()
	if !stillRecording {
		a.recorder.Cancel()
	}

	a.mu.Lock()
	a.starting = false
	a.mu.Unlock()
}

// startRecorderWithTimeout guards against audio drivers that hang on start.
func (a *App) startRecorderWithTimeout(device string, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- a.recorder.Start(device) }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		// If the start eventually completes, tear it down.
		go func() {
			if err := <-done; err == nil {
				a.recorder.Cancel()
			}
		}()
		return errors.New("timed out starting audio capture")
	}
}

// waveformLoop feeds live levels to the overlay while recording.
func (a *App) waveformLoop() {
	src, ok := a.recorder.(WaveformSource)
	if !ok {
		return
	}
	if !a.waveformActive.CompareAndSwap(false, true) {
		return
	}
	defer a.waveformActive.Store(false)

	agg := newWaveformAgg(waveformBars)
	ticker := time.NewTicker(waveformInterval * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if !a.IsRecording() {
			return
		}
		samples := src.TakeWaveformSamples()
		if len(samples) == 0 {
			continue
		}
		values, rawPeak := agg.update(samples)
		a.overlay.UpdateWaveform(values, rawPeak >= silenceThreshold)
	}
}

// stopRecordingInternal stops the capture and returns the samples (nil when
// nothing usable was captured).
func (a *App) stopRecordingInternal() []int16 {
	a.mu.Lock()
	if !a.recording {
		a.mu.Unlock()
		return nil
	}
	a.recording = false
	starting := a.starting
	a.mu.Unlock()

	a.overlay.SetTranscribing(true)
	a.playSound(SoundStop)
	info("Stopped")

	if starting {
		deadline := time.Now().Add(maxStartingTime)
		for time.Now().Before(deadline) {
			a.mu.Lock()
			st := a.starting
			a.mu.Unlock()
			if !st {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		a.mu.Lock()
		stuck := a.starting
		if stuck {
			a.starting = false
		}
		a.mu.Unlock()
		if stuck {
			warn("Recording start still pending; discarding this recording")
			a.recorder.Cancel()
			return nil
		}
	}

	samples, err := a.recorder.Stop()
	if err != nil {
		errorf("Failed to stop recording: %v", err)
		a.recorder.Cancel()
		return nil
	}
	return samples
}

// CancelRecording discards an in-progress recording.
func (a *App) CancelRecording() {
	a.mu.Lock()
	if !a.recording {
		a.mu.Unlock()
		return
	}
	a.recording = false
	a.mu.Unlock()

	a.overlay.Hide()
	a.playSound(SoundCancel)
	errorf("Recording cancelled")
	a.recorder.Cancel()
}

// AbortRecording silently discards an in-progress recording (used when
// switching into replay mode: no sound, overlay stays up for the picker).
func (a *App) AbortRecording() {
	a.mu.Lock()
	if !a.recording {
		a.mu.Unlock()
		return
	}
	a.recording = false
	a.mu.Unlock()

	debugf("recording aborted for replay mode")
	a.recorder.Cancel()
}

// CancelTranscription aborts an in-flight transcription (best-effort).
func (a *App) CancelTranscription() {
	a.mu.Lock()
	if !a.processing {
		a.mu.Unlock()
		return
	}
	a.opID++
	a.processing = false
	cancel := a.cancelOp
	a.cancelOp = nil
	a.mu.Unlock()

	info("Cancelling...")
	if cancel != nil {
		cancel()
	}
	a.overlay.SetTranscribing(false)
	a.overlay.Hide()
}

func (a *App) stale(op int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return op != a.opID || !a.processing
}

// beginOp marks the app as processing; returns the op id and false when busy.
func (a *App) beginOp() (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.processing {
		return 0, false
	}
	a.processing = true
	a.opID++
	return a.opID, true
}

// finishOp clears processing state and the overlay if op is still current.
func (a *App) finishOp(op int) {
	a.mu.Lock()
	current := op == a.opID
	if current {
		a.processing = false
	}
	a.mu.Unlock()
	if current {
		a.overlay.SetTranscribing(false)
		a.overlay.Hide()
	}
}

// ProcessRecording stops the capture, transcribes, and types the result.
func (a *App) ProcessRecording(sendEnter bool) {
	op, ok := a.beginOp()
	if !ok {
		debugf("process ignored: already processing")
		return
	}
	defer a.finishOp(op)

	samples := a.stopRecordingInternal()
	if a.stale(op) {
		return
	}

	cfg := a.snap()
	frames := len(samples)
	peak := peakOf(samples)
	var wav []byte
	var transcribed string

	switch {
	case frames == 0:
		warn("No audio captured, skipping...")
	case frames < int(minSeconds*sampleRate):
		warn("Recording too short, skipping...")
	case peak < silenceThreshold:
		warn("Audio too quiet (silence), skipping...")
	default:
		wav = encodeWAV(samples, sampleRate)
		// Always keep the raw WAV for replay before talking to the server,
		// so a crashed server doesn't lose the recording.
		var recentPath string
		if a.recent != nil {
			recentPath = a.recent.Save(wav)
		}
		transcribed = a.transcribeAndType(op, wav, sendEnter)
		if a.stale(op) {
			return
		}
		if transcribed != "" && a.recent != nil {
			a.recent.SetTranscript(recentPath, transcribed)
		}
	}

	if wav != nil && cfg.KeepRecordings {
		archiveRecording(wav, transcribed, cfg.RecordingsDir, cfg.RecordingsMaxBytes)
	}
	printReady(cfg.Hotkey)
}

// ReplayRecording re-sends a previously saved WAV to the server and types the
// result, exactly like a fresh recording.
func (a *App) ReplayRecording(path string, sendEnter bool) {
	a.mu.Lock()
	if a.processing || a.recording || a.starting {
		a.mu.Unlock()
		debugf("replay ignored: busy")
		return
	}
	a.processing = true
	a.opID++
	op := a.opID
	a.mu.Unlock()
	defer a.finishOp(op)

	wav, err := os.ReadFile(path)
	if err != nil {
		errorf("Failed to read recording: %v", err)
		return
	}
	info("Replaying %s", filepath.Base(path))
	a.overlay.SetTranscribing(true)

	text := a.transcribeAndType(op, wav, sendEnter)
	if text != "" && a.recent != nil {
		a.recent.SetTranscript(path, text)
	}
	printReady(a.snap().Hotkey)
}

// transcribeAndType runs the transcription (with retries), applies transforms,
// and types the result. Returns the final text, or "".
func (a *App) transcribeAndType(op int, wav []byte, sendEnter bool) string {
	text := a.transcribeWithRetries(op, wav)
	if a.stale(op) {
		return ""
	}
	if text == "" {
		info("No transcription returned")
		return ""
	}
	text = transformText(text)
	a.typeText(text, sendEnter)
	info("✓ %s", text)
	return text
}

func (a *App) transcribeWithRetries(op int, wav []byte) string {
	cfg := a.snap()
	timeout := time.Duration(cfg.TimeoutS) * time.Second
	for attempt := 1; attempt <= transcribeTries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)

		a.mu.Lock()
		if op != a.opID || !a.processing {
			a.mu.Unlock()
			cancel()
			return ""
		}
		a.cancelOp = cancel
		tr := a.transcriber
		a.mu.Unlock()

		info("Transcribing...")
		text, err := tr.Transcribe(ctx, wav, cfg.Language, cfg.Prompt)
		cancel()

		if err == nil {
			return strings.TrimSpace(text)
		}
		if errors.Is(err, context.Canceled) {
			// Cancelled by the user; don't retry.
			return ""
		}
		if attempt < transcribeTries {
			warn("Transcription failed (%v), retrying (%d/%d)...", err, attempt+1, transcribeTries)
			continue
		}
		errorf("Transcription error after all retries: %v", err)
	}
	return ""
}

func (a *App) typeText(text string, sendEnter bool) {
	if text == "" {
		return
	}
	suffix := ""
	if sendEnter {
		suffix = " ↵"
	}
	info("Typing: %s%s", text, suffix)
	if err := a.inject(text, sendEnter); err != nil {
		errorf("Failed to type text: %v", err)
	}
}

// peakOf returns the peak amplitude normalized to [0, 1].
func peakOf(samples []int16) float64 {
	maxAbs := 0
	for _, s := range samples {
		v := int(s)
		if v < 0 {
			v = -v
		}
		if v > maxAbs {
			maxAbs = v
		}
	}
	return float64(maxAbs) / 32768.0
}
