package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/daemonless/fjord/pkg/catalog"
	"github.com/daemonless/fjord/pkg/stack"
)

// copyStackIcon copies a catalog icon file into a stack dir as icon.<ext>, so
// the stack keeps its picture after the app leaves the catalog or the catalog
// source is removed. No icon, or a failed copy, is not an error -- the UI
// falls back to a letter tile.
func copyStackIcon(dir, src string) {
	if src == "" {
		return
	}
	in, err := os.Open(src)
	if err != nil {
		return
	}
	defer in.Close()
	out, err := os.Create(filepath.Join(dir, "icon"+strings.ToLower(filepath.Ext(src))))
	if err != nil {
		return
	}
	defer out.Close()
	io.Copy(out, in)
}

// backfillStackIcons gives every catalog-installed stack that has no icon file
// one from the catalog cache -- stacks installed before icons were kept with
// the stack. Best-effort, once per start.
func backfillStackIcons(m *stack.Manager, cat *catalog.Cache) {
	stacks, err := m.List()
	if err != nil {
		return
	}
	n := 0
	for _, st := range stacks {
		if st.Icon != "" || st.State == nil || st.State.Origin.AppID == "" {
			continue
		}
		if src := cat.AppIconPath(st.State.Origin.AppID); src != "" {
			copyStackIcon(st.Dir, src)
			n++
		}
	}
	if n > 0 {
		log.Printf("stack icons: copied %d from the catalog cache", n)
	}
}

// stackIcon serves GET /api/stacks/<name>/icon from the stack dir.
func (s *server) stackIcon(w http.ResponseWriter, r *http.Request, name string) {
	st, err := s.manager.Get(name)
	if err != nil || st.Icon == "" {
		http.NotFound(w, r)
		return
	}
	// The same third-party SVG the catalog served, copied into the stack dir:
	// sandbox it the same way (catalog_handlers.go does) or opening it runs
	// its scripts in fjord's origin.
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, filepath.Join(st.Dir, st.Icon))
}
