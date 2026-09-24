package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// firstSeen remembers when fjord first saw each update candidate -- a
// registry digest for a rebuild, "repo:tag" for a version bump -- so a soak
// time can be measured from it. Not the image's build date: reproducible
// builds stamp that 1970, and no registry says when a digest was published.
type firstSeen struct {
	mu   sync.Mutex
	path string
	at   map[string]time.Time
}

func newFirstSeen(fjordRoot string) *firstSeen {
	f := &firstSeen{path: filepath.Join(fjordRoot, "update-first-seen.json"), at: map[string]time.Time{}}
	if b, err := os.ReadFile(f.path); err == nil {
		_ = json.Unmarshal(b, &f.at)
	}
	return f
}

// Mark records key as seen now unless it was seen before, and returns when it
// was first seen.
func (f *firstSeen) Mark(key string) time.Time {
	if f == nil || key == "" { // tests build servers without one
		return time.Time{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.at[key]; ok {
		return t
	}
	now := time.Now().UTC()
	f.at[key] = now
	f.save()
	return now
}

// save writes the table atomically, dropping entries older than 90 days: a
// candidate unseen that long has been superseded many times over.
func (f *firstSeen) save() {
	cutoff := time.Now().Add(-90 * 24 * time.Hour)
	for k, t := range f.at {
		if t.Before(cutoff) {
			delete(f.at, k)
		}
	}
	b, err := json.MarshalIndent(f.at, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".update-first-seen.*.tmp")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil || tmp.Close() != nil {
		return
	}
	os.Rename(tmp.Name(), f.path)
}
