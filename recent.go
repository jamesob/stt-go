package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RecentStore keeps the raw WAVs of the last few recordings on disk so they
// can be replayed to the server later (e.g. after the server crashed). It is
// independent of the KEEP_RECORDINGS archive and persists across restarts.
type RecentStore struct {
	mu  sync.Mutex
	dir string
	max int
}

type RecentEntry struct {
	Path       string
	When       time.Time
	Seconds    float64
	Transcript string
}

func newRecentStore(dir string, max int) *RecentStore {
	return &RecentStore{dir: dir, max: max}
}

// Save writes a WAV into the store and prunes old entries. Returns the saved
// path, or "" on failure.
func (s *RecentStore) Save(wav []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return ""
	}
	now := time.Now()
	name := fmt.Sprintf("rec_%s_%03d.wav", now.Format("20060102_150405"), now.Nanosecond()/1e6)
	path := filepath.Join(s.dir, name)
	if err := os.WriteFile(path, wav, 0o600); err != nil {
		return ""
	}
	s.pruneLocked()
	return path
}

// SetTranscript stores the transcription next to a saved WAV so the replay
// picker can show what was said.
func (s *RecentStore) SetTranscript(path, text string) {
	if path == "" || text == "" {
		return
	}
	_ = os.WriteFile(strings.TrimSuffix(path, ".wav")+".txt", []byte(text), 0o600)
}

// List returns up to max entries, newest first.
func (s *RecentStore) List() []RecentEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []RecentEntry
	for _, name := range s.sortedNamesLocked() {
		if len(out) >= s.max {
			break
		}
		path := filepath.Join(s.dir, name)
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		e := RecentEntry{Path: path, When: fi.ModTime()}
		if t, ok := timeFromRecentName(name); ok {
			e.When = t
		}
		if fi.Size() > 44 {
			// 16-bit mono PCM after the 44-byte WAV header.
			e.Seconds = float64(fi.Size()-44) / float64(sampleRate*2)
		}
		if b, err := os.ReadFile(strings.TrimSuffix(path, ".wav") + ".txt"); err == nil {
			e.Transcript = strings.TrimSpace(string(b))
		}
		out = append(out, e)
	}
	return out
}

// sortedNamesLocked returns rec_*.wav names, newest first. The timestamped
// naming makes lexicographic order chronological.
func (s *RecentStore) sortedNamesLocked() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "rec_") && strings.HasSuffix(name, ".wav") {
			names = append(names, name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names
}

func (s *RecentStore) pruneLocked() {
	names := s.sortedNamesLocked()
	for i, name := range names {
		if i < s.max {
			continue
		}
		path := filepath.Join(s.dir, name)
		if err := os.Remove(path); err == nil {
			_ = os.Remove(strings.TrimSuffix(path, ".wav") + ".txt")
		}
	}
}

// timeFromRecentName parses "rec_20060102_150405_123.wav".
func timeFromRecentName(name string) (time.Time, bool) {
	base := strings.TrimSuffix(strings.TrimPrefix(name, "rec_"), ".wav")
	// Drop the millisecond suffix.
	if i := strings.LastIndex(base, "_"); i > 0 && len(base)-i == 4 {
		base = base[:i]
	}
	t, err := time.ParseInLocation("20060102_150405", base, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// replayLabel formats a picker line like "⏪ 2/3  14:32:05 · 3.4s · “hello…”".
func replayLabel(e RecentEntry, index, total int) string {
	label := fmt.Sprintf("%d/%d  %s · %.1fs", index+1, total, e.When.Format("15:04:05"), e.Seconds)
	if e.Transcript != "" {
		snippet := []rune(e.Transcript)
		if len(snippet) > 18 {
			snippet = append(snippet[:18], '…')
		}
		label += " · “" + string(snippet) + "”"
	}
	return label
}
