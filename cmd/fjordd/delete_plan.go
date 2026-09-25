package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/stack"
)

// deletePlan is what deleting a stack removes and what it leaves, shown
// before the operator confirms. Delete used to say only "bind-mounted data
// stays on disk", which was safe but left every deleted app's folder behind
// with nothing on screen saying where.
type deletePlan struct {
	Containers []string `json:"containers"`
	StackDir   string   `json:"stackDir"`
	// AppData is the stack's own folders: removed only when asked.
	AppData []dataDir `json:"appData"`
	// Keeps is everything else it uses, which delete never touches.
	Keeps []keptPath `json:"keeps"`
}

type dataDir struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	// More: the count stopped before the end (a huge folder); Bytes is a floor.
	More bool `json:"more,omitempty"`
}

type keptPath struct {
	Path string `json:"path"`
	Why  string `json:"why"`
}

// sizeBudget bounds how long a preview spends adding up one folder: a media
// server's metadata can be millions of files, and the dialog must not hang.
const sizeBudget = 2 * time.Second

// planDelete works out a stack's delete plan. Its own data is a folder named
// after the stack in an app-data location -- where installs put it -- that
// the stack binds into and no other stack does. Anything else it binds
// (media, shared config, system files) is someone else's and only listed.
func (s *server) planDelete(ctx context.Context, st *stack.Stack) deletePlan {
	plan := deletePlan{StackDir: filepath.Join(s.manager.StacksDir, st.Name), AppData: []dataDir{}, Keeps: []keptPath{}}
	if status, err := s.backendFor(st).Status(ctx, st); err == nil {
		for _, c := range status.Containers {
			plan.Containers = append(plan.Containers, c.Name)
		}
	}
	mounts := composepkg.Mounts(st.Compose, envMap(st.Env))
	others := s.otherStacksBinds(st.Name)
	own := map[string]bool{}
	for _, loc := range s.dataLocations() {
		dir := filepath.Join(loc, st.Name)
		if own[dir] || !isRealDir(dir) || !bindsInside(mounts, dir) || anyInside(others, dir) {
			continue
		}
		own[dir] = true
		n, more := dirSize(dir, sizeBudget)
		plan.AppData = append(plan.AppData, dataDir{Path: dir, Bytes: n, More: more})
	}
	seen := map[string]bool{}
	for _, m := range mounts {
		if seen[m.Source] {
			continue
		}
		seen[m.Source] = true
		switch {
		case m.Kind == "volume":
			plan.Keeps = append(plan.Keeps, keptPath{m.Source, "named volume -- remove it on the Volumes page"})
		case !filepath.IsAbs(m.Source) || insideAny(m.Source, own):
			// relative binds live in the stack dir; own data is listed above
		case anyInside(others, m.Source) || containsAny(others, m.Source):
			plan.Keeps = append(plan.Keeps, keptPath{m.Source, "also used by another stack"})
		default:
			plan.Keeps = append(plan.Keeps, keptPath{m.Source, "not this stack's own folder"})
		}
	}
	sort.Slice(plan.Keeps, func(i, j int) bool { return plan.Keeps[i].Path < plan.Keeps[j].Path })
	return plan
}

// dataLocations is every folder an app's data may have been put in: the
// configured locations, plus the older defaults stacks still point at.
func (s *server) dataLocations() []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range append(s.appDataLocations(), os.Getenv("FJORD_STORAGE_BASE"),
		filepath.Join(s.fjordRoot, "containers"), filepath.Join(s.fjordRoot, "volumes")) {
		l = filepath.Clean(l)
		if l != "." && l != "/" && filepath.IsAbs(l) && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// otherStacksBinds is every absolute bind source of every other stack.
func (s *server) otherStacksBinds(except string) []string {
	var out []string
	list, _ := s.manager.List()
	for _, o := range list {
		if o.Name == except {
			continue
		}
		full, err := s.manager.Get(o.Name)
		if err != nil {
			continue
		}
		for _, m := range composepkg.Mounts(full.Compose, envMap(full.Env)) {
			if m.Kind == "bind" && filepath.IsAbs(m.Source) {
				out = append(out, filepath.Clean(m.Source))
			}
		}
	}
	return out
}

// within says whether path is dir or below it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func bindsInside(mounts []composepkg.Mount, dir string) bool {
	for _, m := range mounts {
		if m.Kind == "bind" && filepath.IsAbs(m.Source) && within(m.Source, dir) {
			return true
		}
	}
	return false
}

func anyInside(paths []string, dir string) bool {
	for _, p := range paths {
		if within(p, dir) {
			return true
		}
	}
	return false
}

// containsAny: some path in paths is dir itself or a parent of it.
func containsAny(paths []string, dir string) bool {
	for _, p := range paths {
		if within(dir, p) {
			return true
		}
	}
	return false
}

func insideAny(path string, dirs map[string]bool) bool {
	for d := range dirs {
		if within(path, d) {
			return true
		}
	}
	return false
}

// isRealDir: a directory, and not a symlink -- deleting through a link would
// remove whatever it points at.
func isRealDir(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.IsDir()
}

// dirSize adds up a folder's file sizes, giving up after budget.
func dirSize(dir string, budget time.Duration) (int64, bool) {
	deadline := time.Now().Add(budget)
	var n int64
	more := false
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			more = true
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n, more
}

// stackDeletePreview answers GET .../delete-preview.
func (s *server) stackDeletePreview(w http.ResponseWriter, name string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.planDelete(ctx, st))
}
