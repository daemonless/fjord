package main

import (
	"encoding/json"
	"github.com/daemonless/fjord/pkg/manifest"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daemonless/fjord/pkg/doctor"
)

// browseEntry is one item in a directory listing.
type browseEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
}

// handleBrowse serves a host-filesystem directory picker for path variables:
//
//	GET  /api/browse?path=/containers   -> list directories under path
//	POST /api/browse  {"path","name"}   -> mkdir path/name
//
// Host mode only: a containerized fjordd only sees inside its own container, so
// browsing the host is meaningless there -- it returns 409 and the UI falls
// back to a plain text field. fjordd runs as root on the host, so this exposes
// the whole filesystem; that's intended for a single-admin homelab tool.
func (s *server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if doctor.Mode() != "host" {
		http.Error(w, "file browsing is only available when fjordd runs on the host", http.StatusConflict)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.browseList(w, r)
	case http.MethodPost:
		s.browseMkdir(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// browseList returns the sub-directories of a path (files omitted -- path vars
// are always directories). Defaults to the fjord root's parent so the picker
// opens somewhere sensible. Only directories are listed; symlinks are followed
// via os.Stat so mounted trees resolve.
func (s *server) browseList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		http.Error(w, "path must be absolute", http.StatusBadRequest)
		return
	}
	// Climb to the nearest existing directory so a not-yet-created suggestion
	// (e.g. /containers/sonarr/tv) still opens something browsable instead of a
	// dead end. The UI offers to create the missing tail.
	requested := path
	for {
		if fi, err := os.Stat(path); err == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			break // reached "/"
		}
		path = parent
	}
	items, err := os.ReadDir(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	entries := []browseEntry{}
	for _, it := range items {
		isDir := it.IsDir()
		if !isDir { // resolve symlinks to dirs
			if fi, err := os.Stat(filepath.Join(path, it.Name())); err == nil {
				isDir = fi.IsDir()
			}
		}
		if isDir {
			entries = append(entries, browseEntry{Name: it.Name(), IsDir: true})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	parent := filepath.Dir(path)
	if parent == path {
		parent = "" // at "/" -- no parent
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"path":      path, // nearest existing dir being listed
		"parent":    parent,
		"entries":   entries,
		"requested": requested, // original path asked for (differs when climbed)
	})
}

// browseMkdir creates a directory under path, chowned to the default service
// uid/gid so the container can write to it, and returns the new full path.
func (s *server) browseMkdir(w http.ResponseWriter, r *http.Request) {
	var req struct{ Path, Name string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}
	base := filepath.Clean(req.Path)
	if !filepath.IsAbs(base) {
		http.Error(w, "path must be absolute", http.StatusBadRequest)
		return
	}
	// A relative tail under base -- may be several levels ("sonarr/tv"), but must
	// not escape it (no leading "/" or ".." after cleaning).
	name := filepath.Clean(req.Name)
	if name == "" || name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
		http.Error(w, "invalid directory name", http.StatusBadRequest)
		return
	}
	full := filepath.Join(base, name)
	// Creating dirs (and chowning them to the service uid) as root is only
	// offered inside fjord's own storage: the App data locations and any
	// folder-set folder. Anywhere else, the operator creates it by hand.
	if !s.underManagedStorage(full) {
		http.Error(w, "fjord only creates folders under its App data locations or folder sets; create "+full+" by hand", http.StatusForbidden)
		return
	}
	// Only the levels it makes are the service's, and never through a link a
	// container could have planted in its folder (review 1.3).
	if _, err := manifest.MkdirOwned(full, 0o755, 1000, 1000); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"path": full})
}

// underManagedStorage reports whether p lies inside an App data location, a
// folder-set folder, or the fjord root.
func (s *server) underManagedStorage(p string) bool {
	roots := append([]string{s.fjordRoot}, s.appDataLocations()...)
	for _, fs := range loadSettings(s.fjordRoot).FolderSets {
		for _, f := range fs.Folders {
			if strings.HasPrefix(f, "/") && !strings.Contains(f, "{{") {
				roots = append(roots, f)
			}
		}
	}
	p = filepath.Clean(p)
	for _, root := range roots {
		root = filepath.Clean(root)
		if root == "/" {
			continue
		}
		if p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}
