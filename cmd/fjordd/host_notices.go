package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A hostNotice says what fjord changed on the host with nobody asking, so
// the person who runs it can see it happened and where to look. Shown on the
// System page until dismissed.
type hostNotice struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	Title string    `json:"title"`
	Lines []string  `json:"lines"`
}

var hostNoticesMu sync.Mutex

func (s *server) hostNoticesPath() string {
	return filepath.Join(s.fjordRoot, "host-notices.json")
}

func (s *server) hostNotices() []hostNotice {
	hostNoticesMu.Lock()
	defer hostNoticesMu.Unlock()
	return s.readHostNotices()
}

func (s *server) readHostNotices() []hostNotice {
	var out []hostNotice
	if data, err := os.ReadFile(s.hostNoticesPath()); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func (s *server) writeHostNotices(n []hostNotice) {
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return
	}
	tmp := s.hostNoticesPath() + ".tmp"
	if os.WriteFile(tmp, append(data, '\n'), 0o600) == nil {
		_ = os.Rename(tmp, s.hostNoticesPath())
	}
}

// noteHost adds lines to the notice with this id, making it when it is not
// there: one notice per subject, however many passes add to it.
func (s *server) noteHost(id, title string, lines ...string) {
	if len(lines) == 0 {
		return
	}
	hostNoticesMu.Lock()
	defer hostNoticesMu.Unlock()
	all := s.readHostNotices()
	for i := range all {
		if all[i].ID != id {
			continue
		}
		for _, l := range lines {
			if !contains(all[i].Lines, l) {
				all[i].Lines = append(all[i].Lines, l)
			}
		}
		s.writeHostNotices(all)
		return
	}
	s.writeHostNotices(append(all, hostNotice{ID: id, At: time.Now(), Title: title, Lines: lines}))
}

// dropHostNotice removes the notice with this id, if there is one.
func (s *server) dropHostNotice(id string) {
	hostNoticesMu.Lock()
	defer hostNoticesMu.Unlock()
	all := s.readHostNotices()
	kept := all[:0]
	for _, n := range all {
		if n.ID != id {
			kept = append(kept, n)
		}
	}
	if len(kept) != len(all) {
		s.writeHostNotices(kept)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// handleHostNotices: GET lists them, DELETE /api/host-notices/<id> dismisses one.
func (s *server) handleHostNotices(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/host-notices"), "/")
	switch {
	case r.Method == http.MethodGet && id == "":
		n := s.hostNotices()
		if n == nil {
			n = []hostNotice{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(n)
	case r.Method == http.MethodDelete && id != "":
		s.dropHostNotice(id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
