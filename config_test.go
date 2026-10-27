package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := `
# a comment
PLAIN=value
QUOTED="hello world"
SINGLE='single quoted'
export EXPORTED=yes
SPACES =  padded
EMPTY=
NOEQUALS
=NOKEY
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m := parseEnvFile(path)

	want := map[string]string{
		"PLAIN":    "value",
		"QUOTED":   "hello world",
		"SINGLE":   "single quoted",
		"EXPORTED": "yes",
		"SPACES":   "padded",
		"EMPTY":    "",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	if _, ok := m["NOEQUALS"]; ok {
		t.Error("line without = should be skipped")
	}
}

func TestParseEnvFileMissing(t *testing.T) {
	m := parseEnvFile("/nonexistent/.env")
	if len(m) != 0 {
		t.Fatal("missing file should yield empty map")
	}
}

func TestConfigDirMatchesPythonVersion(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got := configDir(); got != "/tmp/xdg/stt" {
		t.Fatalf("configDir = %q, want /tmp/xdg/stt", got)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Chdir(t.TempDir()) // avoid picking up a real ./.env

	cfg, path, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != defaultBaseURL {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Model != defaultModel || cfg.Language != "en" || cfg.Hotkey != "cmd_r" {
		t.Errorf("defaults wrong: %+v", cfg)
	}
	if !cfg.SoundEnabled || cfg.KeepRecordings {
		t.Errorf("bool defaults wrong: %+v", cfg)
	}
	if cfg.TimeoutS != defaultTimeoutS {
		t.Errorf("TimeoutS = %d", cfg.TimeoutS)
	}
	if cfg.AudioDevice != "" {
		t.Errorf("AudioDevice default = %q", cfg.AudioDevice)
	}
	// Recordings dir matches the Python version; recent dir sits in config.
	if !strings.HasSuffix(cfg.RecordingsDir, ".stt-recordings") {
		t.Errorf("RecordingsDir = %q", cfg.RecordingsDir)
	}
	if cfg.RecentDir != filepath.Join(tmp, "stt", "recent") {
		t.Errorf("RecentDir = %q", cfg.RecentDir)
	}
	if path != filepath.Join(tmp, "stt", ".env") {
		t.Errorf("config path = %q", path)
	}
	// Like the Python version, loading must NOT create the config file;
	// first-run setup goes through the wizard instead.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("loadConfig must not create the config file")
	}
}

func TestLoadConfigPrecedence(t *testing.T) {
	// Global config file (same keys the Python version uses).
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalDir := filepath.Join(xdg, "stt")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	global := "STT_BASE_URL=http://global:1/v1\nLANGUAGE=de\nSTT_MODEL=global-model\nAUDIO_DEVICE=MacBook Pro Microphone\n"
	if err := os.WriteFile(filepath.Join(globalDir, ".env"), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}

	// Local ./.env overrides global.
	cwd := t.TempDir()
	local := "STT_BASE_URL=http://local:2/v1\nLANGUAGE=fr\n"
	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	// OS env overrides both.
	t.Setenv("STT_BASE_URL", "http://env:3/v1")

	cfg, _, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://env:3/v1" {
		t.Errorf("OS env should win: %q", cfg.BaseURL)
	}
	if cfg.Language != "fr" {
		t.Errorf("local .env should beat global: %q", cfg.Language)
	}
	if cfg.Model != "global-model" {
		t.Errorf("global value should apply when not overridden: %q", cfg.Model)
	}
	if cfg.AudioDevice != "MacBook Pro Microphone" {
		t.Errorf("AUDIO_DEVICE not read: %q", cfg.AudioDevice)
	}
}

func TestLoadConfigLegacyPythonKeys(t *testing.T) {
	// An untouched config file from the Python version must work: the
	// whisper.cpp URL and model keys serve as fallbacks.
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalDir := filepath.Join(xdg, "stt")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `PROVIDER=whisper-cpp-http
WHISPER_CPP_HTTP_URL=http://localhost:8080/v1
WHISPER_MODEL=large-v3
GROQ_API_KEY=gsk_ignored
AUDIO_DEVICE=MacBook Pro Microphone
HOTKEY=alt_r
LANGUAGE=en
PROMPT=Claude, Anthropic
SOUND_ENABLED=true
`
	if err := os.WriteFile(filepath.Join(globalDir, ".env"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	cfg, _, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://localhost:8080/v1" {
		t.Errorf("WHISPER_CPP_HTTP_URL fallback not honored: %q", cfg.BaseURL)
	}
	if cfg.Model != "large-v3" {
		t.Errorf("WHISPER_MODEL fallback not honored: %q", cfg.Model)
	}
	if cfg.APIKey != "" {
		t.Errorf("GROQ_API_KEY must not leak into the API key: %q", cfg.APIKey)
	}
	if cfg.Hotkey != "alt_r" || cfg.AudioDevice != "MacBook Pro Microphone" || cfg.Prompt != "Claude, Anthropic" {
		t.Errorf("shared keys not honored: %+v", cfg)
	}

	// STT_* keys win over the legacy fallbacks.
	t.Setenv("STT_BASE_URL", "http://new:8000/v1")
	t.Setenv("STT_MODEL", "whisper-1")
	cfg, _, _ = loadConfig()
	if cfg.BaseURL != "http://new:8000/v1" || cfg.Model != "whisper-1" {
		t.Errorf("STT_* should win over legacy keys: %+v", cfg)
	}
}

func TestSaveConfigUpdatesInPlace(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Chdir(t.TempDir()) // no local .env
	globalDir := filepath.Join(xdg, "stt")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(globalDir, ".env")
	orig := "# my config\nHOTKEY=cmd_r\nLANGUAGE=en\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	// No config.yml and not forced: updates the global .env in place, like
	// the Python version's legacy path.
	got, err := saveConfig("HOTKEY", "alt_r", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("saved to %q, want %q", got, path)
	}
	data, _ := os.ReadFile(path)
	want := "# my config\nHOTKEY=alt_r\nLANGUAGE=en\n"
	if string(data) != want {
		t.Fatalf("content = %q, want %q", data, want)
	}

	// Missing key is appended, everything else preserved.
	if _, err := saveConfig("PROMPT", "hello world", false); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != want+"PROMPT=hello world\n" {
		t.Fatalf("append wrong: %q", data)
	}
}

func TestSaveConfigCreatesYAMLWhenForced(t *testing.T) {
	// Like the Python wizard: force_global writes config.yml (creating it),
	// mapping server keys under backends.default and dropping /v1.
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Chdir(t.TempDir())

	path, err := saveConfig("STT_BASE_URL", "http://x:1/v1", true)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(xdg, "stt", "config.yml") {
		t.Fatalf("path = %q", path)
	}
	data, _ := os.ReadFile(path)
	want := "backends:\n  default:\n    provider: openai\n    openai_base_url: http://x:1\norder:\n- default\n"
	if string(data) != want {
		t.Fatalf("content = %q, want %q", data, want)
	}

	// General keys land at top level; booleans round-trip unquoted.
	if _, err := saveConfig("SOUND_ENABLED", "false", true); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "sound_enabled: false\n") {
		t.Fatalf("bool not stored bare: %q", data)
	}
	if !strings.Contains(string(data), "backends:") {
		t.Fatalf("backends lost: %q", data)
	}
}

func TestSaveConfigPrefersLocalEnvUnlessForced(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cwd := t.TempDir()
	t.Chdir(cwd)
	local := filepath.Join(cwd, ".env")
	if err := os.WriteFile(local, []byte("LANGUAGE=en\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	path, err := saveConfig("LANGUAGE", "de", false)
	if err != nil {
		t.Fatal(err)
	}
	if path != local {
		t.Fatalf("non-forced save went to %q, want local %q", path, local)
	}

	path, err = saveConfig("LANGUAGE", "fr", true)
	if err != nil {
		t.Fatal(err)
	}
	if path == local {
		t.Fatal("forced save must go to the global file")
	}
}

func TestFirstRunMarker(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if !isFirstRun() {
		t.Fatal("fresh config dir should be first run")
	}
	markInitialized()
	if isFirstRun() {
		t.Fatal("marker should end first-run state")
	}
	// Same marker file name the Python version uses.
	if !fileExists(filepath.Join(configDir(), ".initialized")) {
		t.Fatal(".initialized marker missing")
	}
}

func TestMaskAPIKey(t *testing.T) {
	if got := maskAPIKey("gsk_abcdefgh1234"); got != "gsk_********1234" {
		t.Errorf("mask = %q", got)
	}
	if got := maskAPIKey("short"); got != "****" {
		t.Errorf("short mask = %q", got)
	}
}

func TestLoadConfigInvalidHotkeyFallsBack(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv("HOTKEY", "bogus_key")

	cfg, _, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hotkey != "cmd_r" {
		t.Errorf("invalid hotkey should fall back to cmd_r, got %q", cfg.Hotkey)
	}
}

func TestLoadConfigTrimsBaseURLSlash(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv("STT_BASE_URL", "http://host:8000/v1/")

	cfg, _, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://host:8000/v1" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
}

func TestLoadConfigFromYAML(t *testing.T) {
	// A config.yml written by the Python version must work unmodified.
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Chdir(t.TempDir())
	globalDir := filepath.Join(xdg, "stt")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yamlCfg := `hotkey: alt_l
audio_device: pipewire
sound_enabled: false
backends:
  default:
    provider: openai
    openai_base_url: http://stt.j.co:8201
order:
- default
`
	if err := os.WriteFile(filepath.Join(globalDir, "config.yml"), []byte(yamlCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, path, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(globalDir, "config.yml") {
		t.Errorf("path = %q", path)
	}
	if cfg.BaseURL != "http://stt.j.co:8201/v1" {
		t.Errorf("BaseURL = %q (want /v1 appended, like the Python provider)", cfg.BaseURL)
	}
	if cfg.Model != "" {
		t.Errorf("Model = %q (want empty = auto-detect)", cfg.Model)
	}
	if cfg.Hotkey != "alt_l" {
		t.Errorf("Hotkey = %q", cfg.Hotkey)
	}
	if cfg.AudioDevice != "pipewire" {
		t.Errorf("AudioDevice = %q", cfg.AudioDevice)
	}
	if cfg.SoundEnabled {
		t.Error("SoundEnabled should be false")
	}
}

func TestLoadConfigYAMLOvershadowsGlobalEnv(t *testing.T) {
	// Once config.yml exists it replaces the global .env (Python behavior),
	// but ./.env still overrides it and OS env overrides everything.
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	globalDir := filepath.Join(xdg, "stt")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, ".env"), []byte("LANGUAGE=de\nLANGUAGE=de\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "config.yml"), []byte("language: es\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".env"), []byte("PROMPT=hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	cfg, _, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Language != "es" {
		t.Errorf("global .env should be ignored once config.yml exists: %q", cfg.Language)
	}
	t.Setenv("LANGUAGE", "it")
	cfg, _, _ = loadConfig()
	if cfg.Language != "it" {
		t.Errorf("OS env should override config.yml: %q", cfg.Language)
	}
}

func TestLoadConfigYAMLOpenAIModelExplicit(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Chdir(t.TempDir())
	globalDir := filepath.Join(xdg, "stt")
	os.MkdirAll(globalDir, 0o755)
	os.WriteFile(filepath.Join(globalDir, "config.yml"), []byte(
		"backends:\n  default:\n    provider: openai\n    openai_base_url: http://h:1/v1\n    openai_whisper_model: my-model\n    openai_api_key: sk-abc\norder:\n- default\n"), 0o644)

	cfg, _, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://h:1/v1" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Model != "my-model" || cfg.APIKey != "sk-abc" {
		t.Errorf("backend keys not honored: %+v", cfg)
	}
}
