package main

// Platform abstraction. The darwin implementation lives in *_darwin.go, the
// Linux implementation in *_linux.go; anything else gets a stub that errors
// at startup.

// Recorder captures 16kHz mono 16-bit PCM.
type Recorder interface {
	// Start begins capture from the named input device ("" = system default).
	Start(deviceName string) error
	// Stop ends the capture and returns everything recorded since Start.
	Stop() ([]int16, error)
	// Cancel ends the capture and discards the audio. Safe to call anytime.
	Cancel()
}

// WaveformSource is implemented by recorders that can hand out the samples
// captured since the last call, for live waveform display.
type WaveformSource interface {
	TakeWaveformSamples() []int16
}

// KeyEvent is a global keyboard event relevant to the app.
type KeyEvent int

const (
	EvHotkeyDown KeyEvent = iota
	EvHotkeyUp
	EvShiftDown
	EvShiftUp
	EvEsc
	EvReplayNext // Up arrow while the hotkey is held
	EvReplayPrev // Down arrow while the hotkey is held
)

// HotkeyListener watches global key state for the configured hotkey.
type HotkeyListener interface {
	// Start begins listening and delivers events on the channel. The
	// implementation must never block on the channel (drop when full).
	Start(hotkeyID string, events chan<- KeyEvent) error
	Stop()
	// SetHotkey switches the trigger key at runtime.
	SetHotkey(hotkeyID string) error
	// HotkeyHeld reports whether the trigger's modifier class is currently
	// held, from the OS's point of view. Used to recover missed releases.
	HotkeyHeld() bool
}

// Overlay is the on-screen recording indicator.
type Overlay interface {
	Show()
	Hide()
	// UpdateWaveform feeds normalized bar values (0..1) and whether the raw
	// level is above the silence threshold.
	UpdateWaveform(values []float64, aboveThreshold bool)
	SetTranscribing(on bool)
	SetShiftHeld(held bool)
	// SetReplay switches the overlay to replay-picker mode showing the given
	// label; an empty string returns to normal mode.
	SetReplay(text string)
}

type nullOverlay struct{}

func (nullOverlay) Show()                          {}
func (nullOverlay) Hide()                          {}
func (nullOverlay) UpdateWaveform([]float64, bool) {}
func (nullOverlay) SetTranscribing(bool)           {}
func (nullOverlay) SetShiftHeld(bool)              {}
func (nullOverlay) SetReplay(string)               {}

// SoundName identifies one of the feedback sounds.
type SoundName int

const (
	SoundStart SoundName = iota
	SoundStop
	SoundCancel
)

// Backend bundles the platform-specific pieces.
type Backend struct {
	Recorder  Recorder
	Listener  HotkeyListener
	Overlay   Overlay
	Inject    func(text string, sendEnter bool) error
	PlaySound func(SoundName)
	// CheckPermissions returns false if the OS permissions needed for
	// global key capture are missing (prompting the user if possible).
	CheckPermissions func() bool
	// PermissionHelp and ListenerHelp are the guidance printed when
	// CheckPermissions fails or the key listener fails to start.
	PermissionHelp string
	ListenerHelp   string
}

// hotkeyDisplayNames is the canonical hotkey id set (validated everywhere);
// hotkeyName renders an id the way the Python version does on this platform.
var hotkeyDisplayNames = map[string]string{
	"cmd_r":   "Right ⌘",
	"cmd_l":   "Left ⌘",
	"alt_r":   "Right ⌥",
	"alt_l":   "Left ⌥",
	"ctrl_r":  "Right ⌃",
	"ctrl_l":  "Left ⌃",
	"shift_r": "Right ⇧",
	"shift_l": "Left ⇧",
}
