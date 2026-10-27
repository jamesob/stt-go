package main

import (
	"strings"
	"testing"
)

func TestParseYAMLRealWorldConfig(t *testing.T) {
	// Exactly what the Python version's yaml.dump produces for the
	// canonical config (including the same-indent list PyYAML emits).
	src := `language: en
hotkey: alt_l
audio_device: pipewire
prompt: Claude, Anthropic, TypeScript, Go
sound_enabled: false
keep_recordings: true
backends:
  default:
    provider: openai
    openai_base_url: http://stt.j.co:8201
    openai_api_key: "sk:quoted key"
  gpu:
    provider: openai
    openai_base_url: http://gpu:8000/v1
order:
- gpu
- default
`
	m, err := parseYAML(src)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := m.Get("hotkey"); v != "alt_l" {
		t.Errorf("hotkey = %q", v)
	}
	if v, _ := m.Get("prompt"); v != "Claude, Anthropic, TypeScript, Go" {
		t.Errorf("prompt = %q", v)
	}
	if v, ok := m.Get("sound_enabled"); !ok || v != "false" {
		t.Errorf("sound_enabled = %q ok=%v", v, ok)
	}
	if v, ok := m.Get("keep_recordings"); !ok || v != "true" {
		t.Errorf("keep_recordings = %q ok=%v", v, ok)
	}
	backends, ok := m.GetMap("backends")
	if !ok {
		t.Fatal("backends missing")
	}
	def, ok := backends.GetMap("default")
	if !ok {
		t.Fatal("backends.default missing")
	}
	if v, _ := def.Get("openai_base_url"); v != "http://stt.j.co:8201" {
		t.Errorf("openai_base_url = %q", v)
	}
	if v, _ := def.Get("openai_api_key"); v != "sk:quoted key" {
		t.Errorf("openai_api_key = %q", v)
	}
	order, ok := m.GetList("order")
	if !ok || len(order) != 2 || order[0] != "gpu" || order[1] != "default" {
		t.Errorf("order = %v", order)
	}

	// Round-trips: re-parsing the emitted document yields the same data.
	reparsed, err := parseYAML(m.String())
	if err != nil {
		t.Fatal(err)
	}
	if got := reparsed.String(); got != m.String() {
		t.Errorf("round-trip differs:\n%s\n---\n%s", m.String(), got)
	}
	// Canonical order: language before hotkey before backends before order.
	text := m.String()
	for _, pair := range [][2]string{{"language", "hotkey"}, {"hotkey", "audio_device"}, {"audio_device", "prompt"}, {"prompt", "sound_enabled"}, {"sound_enabled", "backends"}, {"backends", "order"}} {
		if strings.Index(text, pair[0]+":") > strings.Index(text, pair[1]+":") {
			t.Errorf("canonical order violated: %s after %s in:\n%s", pair[1], pair[0], text)
		}
	}
}

func TestYAMLEmitQuotesAmbiguousScalars(t *testing.T) {
	m := newYmlMap()
	m.Set("prompt", "yes")        // would parse back as bool
	m.Set("url", "http://h:8000") // plain, colon not followed by space
	m.Set("weird", "value: with colon")
	m.Set("list-ok", "- starts dash")
	if m.String() == "" {
		t.Fatal("empty")
	}
	back, err := parseYAML(m.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"prompt", "url", "weird", "list-ok"} {
		if v, _ := back.Get(k); v != m.vals[k].(string) {
			t.Errorf("%s round-trip: got %q want %q", k, v, m.vals[k].(string))
		}
	}
}

func TestParseYAMLNullKey(t *testing.T) {
	m, err := parseYAML("foo:\nbar: 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := m.Get("foo"); !ok || v != "" {
		t.Errorf("null value = %q ok=%v", v, ok)
	}
	if v, _ := m.Get("bar"); v != "1" {
		t.Errorf("bar = %q", v)
	}
}
