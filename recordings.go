package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// archiveRecording saves the WAV (and transcript, if any) to the recordings
// dir, then enforces the size cap. Returns the saved path or "".
func archiveRecording(wav []byte, text, dir string, maxBytes int64) string {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	stamp := time.Now().Format("20060102_150405")
	dest := filepath.Join(dir, stamp+".wav")
	if err := os.WriteFile(dest, wav, 0o600); err != nil {
		return ""
	}
	if text != "" {
		_ = os.WriteFile(filepath.Join(dir, stamp+".txt"), []byte(text), 0o600)
	}
	enforceRecordingsLimit(dir, maxBytes)
	return dest
}

// enforceRecordingsLimit deletes the oldest recordings until the total size
// of *.wav files is within maxBytes.
func enforceRecordingsLimit(dir string, maxBytes int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	type rec struct {
		path  string
		mtime time.Time
		size  int64
	}
	var files []rec
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".wav") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, rec{filepath.Join(dir, e.Name()), fi.ModTime(), fi.Size()})
		total += fi.Size()
	}
	if total <= maxBytes {
		return
	}

	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, f := range files {
		if total <= maxBytes {
			break
		}
		if err := os.Remove(f.path); err != nil {
			continue
		}
		total -= f.size
		_ = os.Remove(strings.TrimSuffix(f.path, ".wav") + ".txt")
	}
}
