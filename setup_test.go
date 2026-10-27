package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type savedConfig struct {
	m     map[string]string
	order []string
}

func (s *savedConfig) save(key, value string) {
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[key] = value
	s.order = append(s.order, key)
}

func wizardConfig() *Config {
	return &Config{
		BaseURL: "http://old:8000/v1",
		APIKey:  "",
		Model:   "whisper-1",
		Hotkey:  "cmd_r",
	}
}

func TestRunSetupSavesAnswers(t *testing.T) {
	saved := &savedConfig{}
	// Answers: URL, API key, model, device #2, hotkey #3 (alt_r).
	in := strings.NewReader("http://gpu:8000/v1\nsk-12345678901\nlarge-v3\n2\n3\n")
	var out bytes.Buffer

	runSetup(in, &out, wizardConfig(), []string{"Mic A", "Mic B"}, "Mic A", nil, saved.save, true)

	want := map[string]string{
		"STT_BASE_URL": "http://gpu:8000/v1",
		"STT_API_KEY":  "sk-12345678901",
		"STT_MODEL":    "large-v3",
		"AUDIO_DEVICE": "Mic B",
		"HOTKEY":       "alt_r",
	}
	for k, v := range want {
		if saved.m[k] != v {
			t.Errorf("%s = %q, want %q", k, saved.m[k], v)
		}
	}
}

func TestRunSetupDefaultsKeepCurrent(t *testing.T) {
	saved := &savedConfig{}
	cur := wizardConfig()
	cur.APIKey = "sk-currentkey99"
	cur.AudioDevice = "Mic B"
	cur.Hotkey = "alt_r"
	// Bare Enter on everything.
	in := strings.NewReader("\n\n\n\n\n")
	var out bytes.Buffer

	runSetup(in, &out, cur, []string{"Mic A", "Mic B"}, "Mic A", nil, saved.save, true)

	if saved.m["STT_BASE_URL"] != "http://old:8000/v1" {
		t.Errorf("base url = %q", saved.m["STT_BASE_URL"])
	}
	if saved.m["STT_API_KEY"] != "sk-currentkey99" {
		t.Errorf("Enter should keep the current key, got %q", saved.m["STT_API_KEY"])
	}
	if saved.m["STT_MODEL"] != "whisper-1" {
		t.Errorf("model = %q", saved.m["STT_MODEL"])
	}
	if saved.m["AUDIO_DEVICE"] != "Mic B" {
		t.Errorf("Enter should keep the current device, got %q", saved.m["AUDIO_DEVICE"])
	}
	if saved.m["HOTKEY"] != "alt_r" {
		t.Errorf("Enter should keep the current hotkey, got %q", saved.m["HOTKEY"])
	}
}

func TestRunSetupClearsAPIKey(t *testing.T) {
	saved := &savedConfig{}
	cur := wizardConfig()
	cur.APIKey = "sk-oldkey123456"
	in := strings.NewReader("\nnone\n\n\n")
	var out bytes.Buffer

	runSetup(in, &out, cur, nil, "", nil, saved.save, true)

	if saved.m["STT_API_KEY"] != "" {
		t.Errorf("'none' should clear the key, got %q", saved.m["STT_API_KEY"])
	}
}

func TestRunSetupSkipsDeviceWithoutEnumeration(t *testing.T) {
	saved := &savedConfig{}
	in := strings.NewReader("\n\n\n1\n")
	var out bytes.Buffer

	runSetup(in, &out, wizardConfig(), nil, "", nil, saved.save, true)

	if _, ok := saved.m["AUDIO_DEVICE"]; ok {
		t.Error("AUDIO_DEVICE must not be saved when devices can't be listed")
	}
	if saved.m["HOTKEY"] != "cmd_r" {
		t.Errorf("hotkey = %q", saved.m["HOTKEY"])
	}
}

func TestRunSetupInvalidChoiceReprompts(t *testing.T) {
	saved := &savedConfig{}
	// Hotkey menu: "99" then "zzz" are invalid, then "2".
	in := strings.NewReader("\n\n\n99\nzzz\n2\n")
	var out bytes.Buffer

	runSetup(in, &out, wizardConfig(), nil, "", nil, saved.save, true)

	if saved.m["HOTKEY"] != "cmd_l" {
		t.Errorf("hotkey = %q, want cmd_l", saved.m["HOTKEY"])
	}
	if !strings.Contains(out.String(), "Invalid choice") {
		t.Error("invalid input should reprompt")
	}
}

func TestRunSetupProbeReported(t *testing.T) {
	saved := &savedConfig{}
	var probedURL, probedKey string
	probe := func(baseURL, apiKey string) error {
		probedURL, probedKey = baseURL, apiKey
		return errors.New("connection refused")
	}
	in := strings.NewReader("http://gpu:8000/v1\nsk-12345678901\n\n\n")
	var out bytes.Buffer

	runSetup(in, &out, wizardConfig(), nil, "", probe, saved.save, true)

	if probedURL != "http://gpu:8000/v1" || probedKey != "sk-12345678901" {
		t.Errorf("probe got %q %q", probedURL, probedKey)
	}
	if !strings.Contains(out.String(), "not reachable") {
		t.Error("probe failure should be reported")
	}
}

func TestWizardHotkeysAreValid(t *testing.T) {
	for _, hk := range wizardHotkeys() {
		if _, ok := hotkeyDisplayNames[hk.id]; !ok {
			t.Errorf("wizard offers unknown hotkey %q", hk.id)
		}
	}
}
