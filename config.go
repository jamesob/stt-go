package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultBaseURL  = "http://localhost:8000/v1"
	defaultModel    = "whisper-1"
	defaultTimeoutS = 30

	defaultRecordingsMaxBytes = 1 << 30 // 1GB, same as the Python version
	recentMax                 = 3       // raw WAVs kept for replay
)

type Config struct {
	BaseURL            string
	APIKey             string
	Model              string
	Language           string
	Prompt             string
	Hotkey             string
	AudioDevice        string
	SoundEnabled       bool
	KeepRecordings     bool
	RecordingsDir      string
	RecordingsMaxBytes int64
	RecentDir          string
	TimeoutS           int
}

// configDir matches the Python version (~/.config/stt), so an existing
// config keeps working. XDG_CONFIG_HOME is honored when set.
func configDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "stt")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".stt"
	}
	return filepath.Join(home, ".config", "stt")
}

// configYAMLPath is the current canonical config file of the Python version
// (~/.config/stt/config.yml), which supersedes the global .env when present.
func configYAMLPath() string {
	return filepath.Join(configDir(), "config.yml")
}

// yamlToEnv maps config.yml general-setting keys to their env names, like the
// Python version's _YAML_TO_ENV. stt_timeout_s is a Go-side extension.
var yamlToEnv = map[string]string{
	"audio_device":    "AUDIO_DEVICE",
	"language":        "LANGUAGE",
	"hotkey":          "HOTKEY",
	"prompt":          "PROMPT",
	"sound_enabled":   "SOUND_ENABLED",
	"keep_recordings": "KEEP_RECORDINGS",
	"stt_timeout_s":   "STT_TIMEOUT_S",
}

var envToYAML = func() map[string]string {
	m := make(map[string]string, len(yamlToEnv))
	for k, v := range yamlToEnv {
		m[v] = k
	}
	return m
}()

func defaultRecordingsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".stt-recordings"
	}
	return filepath.Join(home, ".stt-recordings") // same as the Python version
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// First-run marker, shared with the Python version: if either has completed
// onboarding, the other won't re-run it.
func initializedMarker() string {
	return filepath.Join(configDir(), ".initialized")
}

func isFirstRun() bool {
	return !fileExists(initializedMarker())
}

func markInitialized() {
	_ = os.MkdirAll(configDir(), 0o755)
	_ = os.WriteFile(initializedMarker(), nil, 0o600)
}

// maskAPIKey matches the Python version's display masking.
func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

// parseEnvFile reads a KEY=VALUE file. Missing files yield an empty map.
func parseEnvFile(path string) map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		m[key] = val
	}
	return m
}

// configPaths returns the files watched for live reload.
func configPaths() []string {
	var paths []string
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, ".env"))
	}
	paths = append(paths, configYAMLPath(), filepath.Join(configDir(), ".env"))
	return paths
}

// saveConfig writes a single key, mirroring the Python version's save_config:
// config.yml is used when it exists (or when forceGlobal, which also creates
// it — the Python wizard always does); general keys land as top-level keys and
// server keys under backends.<target>, exactly like save_backend_key. Without
// any YAML it falls back to ./.env (unless forced) or ~/.config/stt/.env.
// Returns the path written.
func saveConfig(key, value string, forceGlobal bool) (string, error) {
	ymlPath := configYAMLPath()
	data, err := parseYAMLFile(ymlPath)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", ymlPath, err)
	}
	if data != nil || forceGlobal {
		return saveYAMLConfig(key, value, ymlPath, data)
	}

	var envPath string
	cwd, err := os.Getwd()
	if err == nil && !forceGlobal && fileExists(filepath.Join(cwd, ".env")) {
		envPath = filepath.Join(cwd, ".env")
	} else {
		dir := configDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		envPath = filepath.Join(dir, ".env")
	}

	var lines []string
	if data, err := os.ReadFile(envPath); err == nil {
		lines = strings.Split(string(data), "\n")
		// Drop the empty element a trailing newline produces.
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}
	}

	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, key+"=") {
			lines[i] = key + "=" + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}

	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		return "", err
	}
	return envPath, nil
}

// saveYAMLConfig applies one key to the config.yml tree and writes it back,
// mirroring the Python version's _save_to_yaml and save_backend_key.
func saveYAMLConfig(key, value, ymlPath string, data *ymlMap) (string, error) {
	if data == nil {
		data = newYmlMap()
	}
	store := func(m *ymlMap, yamlKey, v string) {
		if v == "" {
			m.Remove(yamlKey)
		} else if strings.EqualFold(v, "true") || strings.EqualFold(v, "false") {
			m.put(yamlKey, ymlRaw(strings.ToLower(v)))
		} else {
			m.Set(yamlKey, v)
		}
	}

	switch key {
	case "STT_BASE_URL", "STT_API_KEY", "STT_MODEL":
		backend := yamlTargetBackend(data)
		yamlKey := map[string]string{
			"STT_BASE_URL": "openai_base_url",
			"STT_API_KEY":  "openai_api_key",
			"STT_MODEL":    "openai_whisper_model",
		}[key]
		v := value
		if key == "STT_BASE_URL" && v != "" {
			// The Python version stores the base URL without the /v1
			// suffix (its OpenAI provider appends it).
			v = strings.TrimSuffix(strings.TrimRight(v, "/"), "/v1")
		}
		store(backend, yamlKey, v)
	default:
		if yamlKey, ok := envToYAML[key]; ok {
			store(data, yamlKey, value)
		} else {
			store(data, strings.ToLower(key), value)
		}
	}

	dir := configDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(ymlPath, []byte(data.String()), 0o644); err != nil {
		return "", err
	}
	return ymlPath, nil
}

// yamlTargetBackend returns the mutable backends.<target> map, defaulting to
// backends.default and keeping `order` in sync, like the Python wizard does.
func yamlTargetBackend(data *ymlMap) *ymlMap {
	backends := data.ensureMap("backends")
	target := "default"
	if order, ok := data.GetList("order"); ok {
		for _, name := range order {
			if _, ok := backends.GetMap(name); ok {
				target = name
				break
			}
		}
	} else if len(backends.keys) > 0 {
		target = backends.keys[0]
	}
	backend := backends.ensureMap(target)
	if _, ok := backend.Get("provider"); !ok && (target == "default" || len(backends.keys) == 1) {
		backend.Set("provider", "openai")
	}
	if _, ok := data.GetList("order"); !ok {
		data.SetList("order", []string{target})
	} else {
		order, _ := data.GetList("order")
		found := false
		for _, name := range order {
			if name == target {
				found = true
				break
			}
		}
		if !found {
			data.SetList("order", append(order, target))
		}
	}
	return backend
}

// yamlEnvLayer flattens config.yml into env-style values: general settings
// plus the first backend's server settings, like the Python version's
// load_env_startup + load_provider combined.
func yamlEnvLayer(data *ymlMap) map[string]string {
	m := map[string]string{}
	for yk, ek := range yamlToEnv {
		if v, ok := data.Get(yk); ok {
			m[ek] = v
		}
	}
	backends, ok := data.GetMap("backends")
	if !ok {
		return m
	}
	target := ""
	if order, ok := data.GetList("order"); ok {
		for _, name := range order {
			if _, ok := backends.GetMap(name); ok {
				target = name
				break
			}
		}
	} else if len(backends.keys) > 0 {
		target = backends.keys[0]
	}
	if target == "" {
		return m
	}
	b, ok := backends.GetMap(target)
	if !ok {
		return m
	}
	provider, _ := b.Get("provider")
	if url, _ := b.Get("openai_base_url"); url != "" {
		url = strings.TrimRight(url, "/")
		if !strings.HasSuffix(url, "/v1") {
			url += "/v1" // the Python OpenAI provider appends /v1 too
		}
		m["STT_BASE_URL"] = url
	} else if url, _ := b.Get("whisper_cpp_http_url"); url != "" {
		m["STT_BASE_URL"] = strings.TrimRight(url, "/")
	}
	if k, ok := b.Get("openai_api_key"); ok {
		m["STT_API_KEY"] = k
	}
	switch provider {
	case "openai", "":
		if v, ok := b.Get("openai_whisper_model"); ok {
			m["STT_MODEL"] = v // "" = auto-detect from /v1/models
		} else if v, ok := b.Get("whisper_model"); ok {
			m["STT_MODEL"] = v
		} else {
			m["STT_MODEL"] = "" // configured backend, no model → auto
		}
	case "whisper-cpp-http":
		if v, ok := b.Get("whisper_model"); ok {
			m["STT_MODEL"] = v
		}
	}
	if m["STT_BASE_URL"] == "" && (provider == "groq" || provider == "mlx" || provider == "parakeet") {
		warn("Backend %q uses provider %q, which sttg does not support; using server defaults", target, provider)
	}
	return m
}

// loadConfig reads configuration with the same precedence as the Python
// version: OS environment > ./.env > config.yml (or ~/.config/stt/.env when
// no config.yml exists — the global .env is ignored once YAML is in play).
func loadConfig() (*Config, string, error) {
	dir := configDir()
	globalPath := filepath.Join(dir, ".env")

	var layers []map[string]string
	if cwd, err := os.Getwd(); err == nil {
		layers = append(layers, parseEnvFile(filepath.Join(cwd, ".env")))
	}
	ymlPath := configYAMLPath()
	ymlData, ymlErr := parseYAMLFile(ymlPath)
	if ymlErr != nil {
		warn("Failed to parse %s: %v", ymlPath, ymlErr)
		ymlData = nil
	}
	if ymlData != nil {
		layers = append(layers, yamlEnvLayer(ymlData))
		globalPath = ymlPath
	} else {
		layers = append(layers, parseEnvFile(filepath.Join(dir, ".env")))
	}

	lookup := func(key string) (string, bool) {
		if v, ok := os.LookupEnv(key); ok {
			return v, true
		}
		for _, m := range layers {
			if v, ok := m[key]; ok {
				return v, true
			}
		}
		return "", false
	}
	get := func(key, def string) string {
		if v, ok := lookup(key); ok {
			return v
		}
		return def
	}
	getBool := func(key string, def bool) bool {
		v := get(key, "")
		if v == "" {
			return def
		}
		return strings.EqualFold(v, "true")
	}

	// Server settings, honoring the legacy keys from the Python version's
	// config file when the STT_* keys are absent.
	baseURL := get("STT_BASE_URL", "")
	if baseURL == "" {
		baseURL = get("WHISPER_CPP_HTTP_URL", "")
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	// An explicitly configured empty model means "auto-detect from the
	// server" (Python behavior); an absent one falls back to the default.
	model := ""
	if v, ok := lookup("STT_MODEL"); ok {
		model = v
	} else if v, ok := lookup("WHISPER_MODEL"); ok {
		model = v
	} else {
		model = defaultModel
	}

	cfg := &Config{
		BaseURL:            strings.TrimRight(baseURL, "/"),
		APIKey:             get("STT_API_KEY", ""),
		Model:              model,
		Language:           get("LANGUAGE", "en"),
		Prompt:             get("PROMPT", ""),
		Hotkey:             get("HOTKEY", "cmd_r"),
		AudioDevice:        get("AUDIO_DEVICE", ""),
		SoundEnabled:       getBool("SOUND_ENABLED", true),
		KeepRecordings:     getBool("KEEP_RECORDINGS", false),
		RecordingsDir:      defaultRecordingsDir(),
		RecordingsMaxBytes: defaultRecordingsMaxBytes,
		RecentDir:          filepath.Join(dir, "recent"),
		TimeoutS:           defaultTimeoutS,
	}

	if v := get("STT_TIMEOUT_S", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			cfg.TimeoutS = n
		}
	}

	if _, ok := hotkeyDisplayNames[cfg.Hotkey]; !ok {
		warn("Unknown HOTKEY %q, falling back to cmd_r", cfg.Hotkey)
		cfg.Hotkey = "cmd_r"
	}

	return cfg, globalPath, nil
}

// diffConfigs returns the env-style key names whose values differ.
func diffConfigs(old, new *Config) []string {
	var changes []string
	add := func(key string, differ bool) {
		if differ {
			changes = append(changes, key)
		}
	}
	add("STT_BASE_URL", old.BaseURL != new.BaseURL)
	add("STT_API_KEY", old.APIKey != new.APIKey)
	add("STT_MODEL", old.Model != new.Model)
	add("LANGUAGE", old.Language != new.Language)
	add("PROMPT", old.Prompt != new.Prompt)
	add("HOTKEY", old.Hotkey != new.Hotkey)
	add("AUDIO_DEVICE", old.AudioDevice != new.AudioDevice)
	add("SOUND_ENABLED", old.SoundEnabled != new.SoundEnabled)
	add("KEEP_RECORDINGS", old.KeepRecordings != new.KeepRecordings)
	add("STT_TIMEOUT_S", old.TimeoutS != new.TimeoutS)
	return changes
}
