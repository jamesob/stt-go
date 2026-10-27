package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveRecording(t *testing.T) {
	dir := t.TempDir()
	wav := encodeWAV([]int16{1, 2, 3}, 16000)

	dest := archiveRecording(wav, "the transcript", dir, 1<<30)
	if dest == "" {
		t.Fatal("archive failed")
	}
	data, err := os.ReadFile(dest)
	if err != nil || len(data) != len(wav) {
		t.Fatalf("wav not written correctly: %v", err)
	}
	txt, err := os.ReadFile(strings.TrimSuffix(dest, ".wav") + ".txt")
	if err != nil || string(txt) != "the transcript" {
		t.Fatalf("transcript not written: %v %q", err, txt)
	}
}

func TestArchiveRecordingNoText(t *testing.T) {
	dir := t.TempDir()
	dest := archiveRecording([]byte("wav"), "", dir, 1<<30)
	if dest == "" {
		t.Fatal("archive failed")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".txt") {
			t.Fatal("no txt should be written without a transcript")
		}
	}
}

func TestEnforceRecordingsLimit(t *testing.T) {
	dir := t.TempDir()

	// Three 100-byte recordings with increasing mtimes.
	base := time.Now().Add(-time.Hour)
	for i, name := range []string{"a.wav", "b.wav", "c.wav"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, make([]byte, 100), 0o600); err != nil {
			t.Fatal(err)
		}
		// Companion transcript for the oldest.
		if i == 0 {
			os.WriteFile(filepath.Join(dir, "a.txt"), []byte("t"), 0o600)
		}
		mt := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	// Cap at 250 bytes: the oldest (a.wav) must go, along with its txt.
	enforceRecordingsLimit(dir, 250)

	if _, err := os.Stat(filepath.Join(dir, "a.wav")); !os.IsNotExist(err) {
		t.Fatal("oldest recording should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("companion transcript should have been deleted")
	}
	for _, name := range []string{"b.wav", "c.wav"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s should remain: %v", name, err)
		}
	}
}

func TestEnforceRecordingsLimitUnderCap(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.wav"), make([]byte, 10), 0o600)
	enforceRecordingsLimit(dir, 100)
	if _, err := os.Stat(filepath.Join(dir, "a.wav")); err != nil {
		t.Fatal("file under cap should remain")
	}
}
