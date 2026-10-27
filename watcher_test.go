package main

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfigWatcherFiresOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var fired atomic.Int32
	w := startConfigWatcher([]string{path}, 20*time.Millisecond, func() {
		fired.Add(1)
	})
	defer w.Stop()

	// No change yet.
	time.Sleep(60 * time.Millisecond)
	if fired.Load() != 0 {
		t.Fatal("watcher fired without a change")
	}

	// Modify the file (content size changes, so no mtime-resolution issues).
	if err := os.WriteFile(path, []byte("A=22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return fired.Load() >= 1 })
}

func TestConfigWatcherFiresOnCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	var fired atomic.Int32
	w := startConfigWatcher([]string{path}, 20*time.Millisecond, func() {
		fired.Add(1)
	})
	defer w.Stop()

	if err := os.WriteFile(path, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return fired.Load() >= 1 })
}

func TestDiffConfigs(t *testing.T) {
	a := &Config{BaseURL: "x", Model: "m", Language: "en", Hotkey: "cmd_r", SoundEnabled: true, TimeoutS: 30}
	b := &Config{BaseURL: "x", Model: "m", Language: "en", Hotkey: "cmd_r", SoundEnabled: true, TimeoutS: 30}

	if d := diffConfigs(a, b); len(d) != 0 {
		t.Fatalf("no changes expected, got %v", d)
	}

	b.Language = "de"
	b.Hotkey = "alt_r"
	b.SoundEnabled = false
	d := diffConfigs(a, b)
	want := map[string]bool{"LANGUAGE": true, "HOTKEY": true, "SOUND_ENABLED": true}
	if len(d) != len(want) {
		t.Fatalf("changes = %v", d)
	}
	for _, k := range d {
		if !want[k] {
			t.Fatalf("unexpected change key %q", k)
		}
	}
}
