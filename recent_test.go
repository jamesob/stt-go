package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecentStoreSaveAndList(t *testing.T) {
	store := newRecentStore(t.TempDir(), 3)

	wav := encodeWAV(loudSamples(sampleRate*2), sampleRate) // 2s
	path := store.Save(wav)
	if path == "" {
		t.Fatal("save failed")
	}

	entries := store.List()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Path != path {
		t.Fatalf("path = %q, want %q", e.Path, path)
	}
	if e.Seconds < 1.9 || e.Seconds > 2.1 {
		t.Fatalf("duration = %f, want ~2s", e.Seconds)
	}
	if e.Transcript != "" {
		t.Fatalf("transcript should be empty, got %q", e.Transcript)
	}
	if time.Since(e.When) > time.Minute {
		t.Fatalf("timestamp looks wrong: %v", e.When)
	}
}

func TestRecentStorePrunesToMax(t *testing.T) {
	store := newRecentStore(t.TempDir(), 3)
	var paths []string
	for i := 0; i < 5; i++ {
		p := store.Save(encodeWAV(loudSamples(1000), sampleRate))
		paths = append(paths, p)
		store.SetTranscript(p, "text")
		time.Sleep(5 * time.Millisecond)
	}

	entries := store.List()
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	// Newest first: the last three saved, in reverse order.
	for i := 0; i < 3; i++ {
		want := paths[4-i]
		if entries[i].Path != want {
			t.Fatalf("entries[%d] = %q, want %q", i, entries[i].Path, want)
		}
	}
	// The two oldest WAVs and their transcripts must be gone.
	for _, p := range paths[:2] {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("old recording %s should be pruned", p)
		}
		if _, err := os.Stat(strings.TrimSuffix(p, ".wav") + ".txt"); !os.IsNotExist(err) {
			t.Fatalf("old transcript for %s should be pruned", p)
		}
	}
}

func TestRecentStoreTranscript(t *testing.T) {
	store := newRecentStore(t.TempDir(), 3)
	p := store.Save(encodeWAV(loudSamples(1000), sampleRate))
	store.SetTranscript(p, "hello there")

	entries := store.List()
	if len(entries) != 1 || entries[0].Transcript != "hello there" {
		t.Fatalf("transcript not round-tripped: %+v", entries)
	}
}

func TestRecentStorePersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	store := newRecentStore(dir, 3)
	p := store.Save(encodeWAV(loudSamples(1000), sampleRate))
	store.SetTranscript(p, "survives restart")

	// Simulate a restart: a brand new store over the same directory.
	store2 := newRecentStore(dir, 3)
	entries := store2.List()
	if len(entries) != 1 || entries[0].Transcript != "survives restart" {
		t.Fatalf("entries should persist across instances: %+v", entries)
	}
}

func TestRecentStoreEmptyList(t *testing.T) {
	store := newRecentStore(filepath.Join(t.TempDir(), "nonexistent"), 3)
	if entries := store.List(); len(entries) != 0 {
		t.Fatalf("expected empty list, got %+v", entries)
	}
}

func TestReplayLabel(t *testing.T) {
	e := RecentEntry{
		When:       time.Date(2026, 10, 23, 14, 32, 5, 0, time.Local),
		Seconds:    3.42,
		Transcript: "hello world this is a long transcript",
	}
	label := replayLabel(e, 1, 3)
	if !strings.Contains(label, "2/3") || !strings.Contains(label, "14:32:05") || !strings.Contains(label, "3.4s") {
		t.Fatalf("label = %q", label)
	}
	if !strings.Contains(label, "…") {
		t.Fatalf("long transcript should be truncated: %q", label)
	}

	plain := replayLabel(RecentEntry{When: e.When, Seconds: 1.0}, 0, 1)
	if strings.Contains(plain, "“") {
		t.Fatalf("no quotes expected without transcript: %q", plain)
	}
}

func TestTimeFromRecentName(t *testing.T) {
	ts, ok := timeFromRecentName("rec_20261023_143205_123.wav")
	if !ok {
		t.Fatal("parse failed")
	}
	if ts.Hour() != 14 || ts.Minute() != 32 || ts.Second() != 5 {
		t.Fatalf("time = %v", ts)
	}
	if _, ok := timeFromRecentName("garbage.wav"); ok {
		t.Fatal("garbage should not parse")
	}
}
