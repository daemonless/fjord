package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/daemonless/fjord/pkg/doctor"
	"github.com/daemonless/fjord/pkg/engine"
)

// descriptor returns the registered descriptor for an engine name, or false.
func descriptor(name string) (engine.Descriptor, bool) {
	for _, d := range engineDescriptors {
		if d.Name == name {
			return d, true
		}
	}
	return engine.Descriptor{}, false
}

// engineInfo is the wire format for one selectable runtime engine, built
// entirely from the engine's own Descriptor.
type engineInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default"`
	Available   bool   `json:"available"`         // the runtime is installed on this host
	Enabled     bool   `json:"enabled"`           // registered + usable (available AND not disabled)
	Reason      string `json:"reason,omitempty"`  // why it can't run, when unavailable
	Warning     string `json:"warning,omitempty"` // usable-with-a-caveat note
	CanInstall  bool   `json:"canInstall,omitempty"`
}

// pinDefaultEngine writes the default that is in effect right now, so adding
// an engine cannot silently take it over.
//
// The stored default survives its engine being uninstalled: another engine
// becomes the effective default meanwhile, and installing the stored one back
// hands the default straight to it -- a change nobody asked for, in the middle
// of an action that was only meant to add a choice. Freezing what is in effect
// makes "install" mean install and nothing else. Setting the default stays an
// explicit act, on the Engines page.
func (s *server) pinDefaultEngine() {
	cur := s.defaultEngine()
	if cur == "" {
		return
	}
	if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.DefaultEngine = cur }); err != nil {
		log.Printf("pinning default engine %s: %v", cur, err)
	}
}

// handleEngine reports the engines this host can run and the default for new
// installs (GET), or sets the default (POST). Every engine describes itself via
// its Descriptor, so this handler names no specific engine.
func (s *server) handleEngine(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		host := doctor.Mode() == "host"
		engines := []engineInfo{}
		for _, d := range engineDescriptors {
			_, registered := s.backend(d.Name)
			ok, reason, warning := d.Available()
			info := engineInfo{
				Name:        d.Name,
				Description: d.Description,
				Default:     d.Name == s.defaultEngine(),
				Available:   ok,
				Enabled:     registered,
				Reason:      reason,
				CanInstall:  !ok && host && d.Package != "",
			}
			if ok {
				info.Warning = warning
			}
			engines = append(engines, info)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"default": s.defaultEngine(),
			"jailed":  engine.Jailed(),
			"engines": engines,
		})

	case http.MethodPost:
		var req struct {
			Default string `json:"default"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload", 400)
			return
		}
		if _, ok := s.backend(req.Default); !ok {
			http.Error(w, "engine not available: "+req.Default, 400)
			return
		}
		if err := updateSettings(s.fjordRoot, func(st *savedSettings) { st.DefaultEngine = req.Default }); err != nil {
			http.Error(w, "persist: "+err.Error(), 500)
			return
		}
		s.setDefaultEngine(req.Default)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"default": req.Default})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// stacksOnEngine returns the display labels of stacks bound to the named engine
// ("<display> (<id>)"), so a blocked disable can name what depends on it.
func (s *server) stacksOnEngine(name string) []string {
	var out []string
	stacks, err := s.manager.List()
	if err != nil {
		return out
	}
	for _, st := range stacks {
		if st.EngineName() == name {
			label := st.DisplayName
			if label == "" || label == st.Name {
				out = append(out, st.Name)
			} else {
				out = append(out, label+" ("+st.Name+")")
			}
		}
	}
	return out
}

// handleEngineToggle enables or disables an engine (Extensions tab). Disabling is
// blocked while stacks are bound to it -- they'd be stranded (every operation
// on them would fail with engine.ErrUnavailable) -- and the response lists them.
func (s *server) handleEngineToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	d, ok := descriptor(req.Name)
	if !ok {
		http.Error(w, "unknown engine: "+req.Name, 400)
		return
	}

	if req.Enabled {
		if avail, reason, _ := d.Available(); !avail {
			http.Error(w, "engine not available on this host: "+reason, http.StatusConflict)
			return
		}
		// Enabling one is adding a choice, not making it the choice.
		s.pinDefaultEngine()
	} else {
		// Refuse to strand stacks that run on this engine.
		if bound := s.stacksOnEngine(req.Name); len(bound) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]any{
				"error":  "Can't disable " + req.Name + " while stacks run on it. Move or delete them first.",
				"stacks": bound,
			})
			return
		}
		// Don't disable the last usable engine -- nothing could run.
		if _, isOn := s.backend(req.Name); isOn && s.engineCount() <= 1 {
			http.Error(w, "can't disable the only enabled engine", http.StatusConflict)
			return
		}
	}

	if err := updateSettings(s.fjordRoot, func(st *savedSettings) {
		st.DisabledEngines = toggleInList(st.DisabledEngines, req.Name, !req.Enabled)
		// Turning off the default leaves it naming an engine that is no longer
		// there: every new stack is then bound to a runtime this host will not
		// start, and the setup page reports a default it cannot use. Clearing
		// it hands the choice back to whatever is still enabled.
		if !req.Enabled && st.DefaultEngine == req.Name {
			st.DefaultEngine = ""
		}
	}); err != nil {
		http.Error(w, "persist: "+err.Error(), 500)
		return
	}
	s.rebuildBackends()
	// Re-pin so the remaining engine is the default, rather than leaving it to
	// whichever the map happens to yield first.
	s.pinDefaultEngine()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "default": s.defaultEngine()})
}

// toggleInList adds name to the list when present==true, removes it otherwise,
// keeping the list deduplicated.
func toggleInList(list []string, name string, present bool) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != name {
			out = append(out, x)
		}
	}
	if present {
		out = append(out, name)
	}
	return out
}

// handleEngineInstall installs an engine's package via pkg (host mode only;
// the package name comes from the engine's Descriptor, so this stays generic).
// The new engine is registered right away; no restart.
func (s *server) handleEngineInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if doctor.Mode() != "host" {
		http.Error(w, "engines can only be installed when fjordd runs on the host", http.StatusConflict)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	d, ok := descriptor(req.Name)
	if !ok || d.Package == "" {
		http.Error(w, "unknown engine: "+req.Name, 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	// Package may list several (appjail + its director); one pkg invocation.
	// Before the new engine can be registered: whatever is default now stays
	// default.
	s.pinDefaultEngine()
	var out bytes.Buffer
	if err := doctor.PkgInstall(ctx, d.Package, &out); err != nil {
		http.Error(w, err.Error()+"\n"+out.String(), http.StatusInternalServerError)
		return
	}
	restart := !s.registerNewEngines()
	if _, ok := s.backend(req.Name); ok {
		restart = false
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "installed", "restartRequired": restart})
}
