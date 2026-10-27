package main

import (
	"os"
	"time"
)

// ConfigWatcher polls the config files for changes (mtime/size), replacing
// the Python version's watchdog-based watcher without a dependency.
type ConfigWatcher struct {
	stop chan struct{}
}

type fileStamp struct {
	mtime  time.Time
	size   int64
	exists bool
}

func statFiles(paths []string) map[string]fileStamp {
	m := make(map[string]fileStamp, len(paths))
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			m[p] = fileStamp{}
			continue
		}
		m[p] = fileStamp{mtime: fi.ModTime(), size: fi.Size(), exists: true}
	}
	return m
}

// startConfigWatcher calls onChange (from the watcher goroutine) whenever any
// of the files changes, appears, or disappears.
func startConfigWatcher(paths []string, interval time.Duration, onChange func()) *ConfigWatcher {
	w := &ConfigWatcher{stop: make(chan struct{})}
	// Snapshot synchronously so changes made right after this call are seen.
	last := statFiles(paths)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
				cur := statFiles(paths)
				changed := false
				for p, st := range cur {
					if st != last[p] {
						changed = true
						break
					}
				}
				if changed {
					last = cur
					onChange()
				}
			}
		}
	}()
	return w
}

func (w *ConfigWatcher) Stop() {
	close(w.stop)
}
