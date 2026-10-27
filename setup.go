package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// The interactive configuration wizard, a port of the Python version's
// onboarding (run_setup): the provider questions are replaced by server
// questions, everything else — numbered menus with current/recommended
// markers, immediate saves per answer, closing summary — matches.

// wizardHotkeys mirrors the order and labels the Python wizard offers
// (get_hotkey_display_name is platform-aware: Super on Linux, ⌘ on macOS).
func wizardHotkeys() []struct{ id, desc string } {
	ids := []string{"cmd_r", "cmd_l", "alt_r", "alt_l", "ctrl_r", "shift_r"}
	out := make([]struct{ id, desc string }, 0, len(ids))
	for _, id := range ids {
		out = append(out, struct{ id, desc string }{id, hotkeyName(id)})
	}
	return out
}

type setupIO struct {
	in  *bufio.Scanner
	out io.Writer
}

func (s *setupIO) printf(format string, args ...any) {
	fmt.Fprintf(s.out, format+"\n", args...)
}

// ask prompts and returns the trimmed answer, or def on a bare Enter.
func (s *setupIO) ask(prompt, def string) string {
	if def != "" {
		fmt.Fprintf(s.out, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(s.out, "%s: ", prompt)
	}
	if !s.in.Scan() {
		return def
	}
	if text := strings.TrimSpace(s.in.Text()); text != "" {
		return text
	}
	return def
}

// askChoice prompts for a 1-based menu choice.
func (s *setupIO) askChoice(prompt string, def, max int) int {
	for {
		raw := s.ask(prompt, strconv.Itoa(def))
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 && n <= max {
			return n
		}
		s.printf("Invalid choice.")
	}
}

// runSetup walks through all settings, saving each via save (like the Python
// wizard saves through save_config). devices/defaultDevice come from the
// platform; probe (optional) checks server reachability; reconfigure only
// changes the banner.
func runSetup(in io.Reader, out io.Writer, current *Config,
	devices []string, defaultDevice string,
	probe func(baseURL, apiKey string) error,
	save func(key, value string), reconfigure bool) {

	s := &setupIO{in: bufio.NewScanner(in), out: out}

	s.printf("")
	if reconfigure {
		s.printf("── STT Configuration ──")
	} else {
		s.printf("── Welcome to STT ──")
		s.printf("Voice-to-text, self-hosted. Hold a key, speak, release — words appear.")
		s.printf("Let's get you set up...")
	}
	s.printf("")

	// Step 1: transcription server (replaces the Python provider selection).
	s.printf("Transcription server (OpenAI-compatible: speaches, LocalAI, vLLM, ...)")
	baseURL := strings.TrimRight(s.ask("Server base URL", orDefaultStr(current.BaseURL, defaultBaseURL)), "/")
	save("STT_BASE_URL", baseURL)

	apiKey := current.APIKey
	keyPrompt := "API key (Enter for none)"
	if apiKey != "" {
		keyPrompt = fmt.Sprintf("API key (Enter to keep %s, 'none' to clear)", maskAPIKey(apiKey))
	}
	switch answer := s.ask(keyPrompt, ""); {
	case answer == "":
		// keep current (possibly empty)
	case strings.EqualFold(answer, "none"):
		apiKey = ""
	default:
		apiKey = answer
	}
	save("STT_API_KEY", apiKey)

	// An empty current model means auto-detect; show no default so a bare
	// Enter keeps it (Python: "Model name (Enter to auto-detect)").
	modelPrompt := "Model name"
	if current.Model == "" {
		modelPrompt = "Model name (Enter to auto-detect from server)"
	}
	model := s.ask(modelPrompt, current.Model)
	save("STT_MODEL", model)

	if probe != nil {
		if err := probe(baseURL, apiKey); err != nil {
			s.printf("⚠️  Server not reachable (%v) — saved anyway", err)
		} else {
			s.printf("✓ Server reachable")
		}
	}
	s.printf("")

	// Step 2: audio device (skipped when the platform can't enumerate).
	device := current.AudioDevice
	if len(devices) > 0 {
		s.printf("Select audio input device:")
		defChoice := 1
		for i, name := range devices {
			var marks []string
			if name == defaultDevice {
				marks = append(marks, "default")
			}
			if current.AudioDevice != "" && name == current.AudioDevice {
				marks = append(marks, "current")
				defChoice = i + 1
			} else if name == defaultDevice && current.AudioDevice == "" {
				defChoice = i + 1
			}
			suffix := ""
			if len(marks) > 0 {
				suffix = "  (" + strings.Join(marks, ", ") + ")"
			}
			s.printf("  %d. %s%s", i+1, name, suffix)
		}
		choice := s.askChoice("Select device", defChoice, len(devices))
		device = devices[choice-1]
		save("AUDIO_DEVICE", device)
		s.printf("")
	}

	// Step 3: hotkey.
	s.printf("Select trigger hotkey:")
	hks := wizardHotkeys()
	defChoice := 1
	for i, hk := range hks {
		marker := ""
		if current.Hotkey == hk.id {
			marker = "  (current)"
			defChoice = i + 1
		} else if i == 0 && current.Hotkey == "" {
			marker = "  (recommended)"
		}
		s.printf("  %d. %s%s", i+1, hk.desc, marker)
	}
	choice := s.askChoice("Select hotkey", defChoice, len(hks))
	hotkey := hks[choice-1]
	save("HOTKEY", hotkey.id)

	// Summary, like the Python wizard's closing panel.
	s.printf("")
	s.printf("── Setup complete ──")
	s.printf("Server:  %s", baseURL)
	s.printf("Model:   %s", model)
	s.printf("Hotkey:  %s", hotkey.desc)
	s.printf("Device:  %s", orDefaultStr(device, "System default"))
	s.printf("")
}

func orDefaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
