package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
)

// leftovers is app data nothing uses any more: what deleting a stack left
// behind before delete could take its data along (and still does, unticked).
type leftovers struct {
	Folders []dataDir `json:"folders"`
	// Unchecked names engines that cannot list their containers' mounts: a
	// folder here may still be used by one of their containers.
	Unchecked []string `json:"unchecked,omitempty"`
}

// findLeftovers lists folders in the app-data locations that no stack is
// named after, no stack binds, and no container on the host mounts -- fjord's
// or not. The last matters: on jupiter /containers also holds the data of
// containers Ansible runs, which no fjord stack knows about.
func (s *server) findLeftovers(ctx context.Context) (leftovers, error) {
	out := leftovers{Folders: []dataDir{}}
	stacks := map[string]bool{}
	var used []string
	list, err := s.manager.List()
	if err != nil {
		return out, err
	}
	for _, st := range list {
		stacks[st.Name] = true
		full, err := s.manager.Get(st.Name)
		if err != nil {
			return out, err // a stack we cannot read might use anything
		}
		for _, m := range composepkg.Mounts(full.Compose, envMap(full.Env)) {
			if m.Kind == "bind" && filepath.IsAbs(m.Source) {
				used = append(used, filepath.Clean(m.Source))
			}
		}
	}
	s.mu.RLock()
	backends := make(map[string]engine.Backend, len(s.backends))
	for k, v := range s.backends {
		backends[k] = v
	}
	s.mu.RUnlock()
	for name, be := range backends {
		ml, ok := be.(engine.MountLister)
		if !ok {
			out.Unchecked = append(out.Unchecked, name)
			continue
		}
		paths, err := ml.HostMounts(ctx)
		if err != nil {
			return out, err // unsure means nothing is offered
		}
		used = append(used, paths...)
	}
	sort.Strings(out.Unchecked)
	for _, loc := range s.dataLocations() {
		entries, _ := os.ReadDir(loc)
		for _, e := range entries {
			dir := filepath.Join(loc, e.Name())
			if strings.HasPrefix(e.Name(), ".") || stacks[e.Name()] || !isRealDir(dir) ||
				anyInside(used, dir) || containsAny(used, dir) {
				continue
			}
			n, more := dirSize(dir, time.Second)
			out.Folders = append(out.Folders, dataDir{Path: dir, Bytes: n, More: more})
		}
	}
	return out, nil
}

// handleLeftovers: GET lists left-over app data; POST {"path": ...} removes
// one folder, only if it is on the list worked out right now.
func (s *server) handleLeftovers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		found, err := s.findLeftovers(ctx)
		if err != nil {
			http.Error(w, "could not work out what is in use: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(found)
	case http.MethodPost:
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			http.Error(w, "path required", 400)
			return
		}
		found, err := s.findLeftovers(ctx)
		if err != nil {
			http.Error(w, "not removed: could not work out what is in use: "+err.Error(), 500)
			return
		}
		for _, f := range found.Folders {
			if f.Path == filepath.Clean(req.Path) {
				if err := os.RemoveAll(f.Path); err != nil {
					http.Error(w, "could not remove "+f.Path+": "+err.Error(), 500)
					return
				}
				log.Printf("removed left-over app data %s", f.Path)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.Error(w, req.Path+" is not left-over app data (a stack or container uses it, or it is not in an app-data location)", 409)
	default:
		http.Error(w, "method not allowed", 405)
	}
}
