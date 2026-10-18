package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/stack"
)

// adopter is implemented by engines that can list containers no stack owns
// and say how to recreate each: podman hands over the run line, appjail a
// director bundle read back from the jail.
type adopter interface {
	UnmanagedContainers(ctx context.Context) ([]engine.Unmanaged, error)
	RemoveContainer(ctx context.Context, name string) error
}

// jailOwner is implemented by engines whose stacks own named jails, so
// fjord's own can be left out of the candidates.
type jailOwner interface {
	StackJails(s *stack.Stack) []string
}

// adoptCandidate is one unmanaged container with the stack it would become.
type adoptCandidate struct {
	engine.Unmanaged
	Engine   string   `json:"engine"`
	Service  string   `json:"service,omitempty"`
	Compose  string   `json:"compose,omitempty"`
	Env      string   `json:"env,omitempty"`
	Director string   `json:"director,omitempty"`
	Makejail string   `json:"makejail,omitempty"`
	Template string   `json:"template,omitempty"`
	Notes    []string `json:"notes,omitempty"`
	Error    string   `json:"error,omitempty"` // why it can't be adopted
}

// adoptSpec resolves what an unmanaged container becomes: the engine's own
// conversion when it made one, else the compose translation of the run line.
func adoptSpec(u engine.Unmanaged) (*engine.AdoptSpec, error) {
	if u.Unadoptable != "" {
		return nil, fmt.Errorf("%s", u.Unadoptable)
	}
	if u.Spec != nil {
		return u.Spec, nil
	}
	if len(u.RunArgs) == 0 {
		return nil, fmt.Errorf("nothing recorded to recreate %s from", u.Name)
	}
	a, err := composepkg.FromRunArgs(u.RunArgs)
	if err != nil {
		return nil, err
	}
	return &engine.AdoptSpec{Service: a.Service, Compose: a.Compose, Env: a.Env, Notes: a.Notes}, nil
}

// ownedJails names every jail fjord's stacks on this engine already own.
func (s *server) ownedJails(be engine.Backend, engineName string) map[string]bool {
	owned := map[string]bool{}
	jo, ok := be.(jailOwner)
	if !ok {
		return owned
	}
	stacks, _ := s.manager.List()
	for _, st := range stacks {
		if st.EngineName() != engineName {
			continue
		}
		for _, j := range jo.StackJails(st) {
			owned[j] = true
		}
	}
	return owned
}

// handleAdopt: GET lists adoptable containers across engines with a preview
// of the stack each would become; POST {container, engine, name, replace}
// creates that stack -- same image, mounts, network address (IP and MAC),
// annotations and, via container_name, the same name -- and with replace
// removes the old container so Start can take its place. The stack is
// created stopped; the caller starts it like any other.
func (s *server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		var out []adoptCandidate
		for _, d := range engineDescriptors {
			be, ok := s.backend(d.Name)
			if !ok {
				continue
			}
			ad, ok := be.(adopter)
			if !ok {
				continue
			}
			list, err := ad.UnmanagedContainers(ctx)
			if err != nil {
				log.Printf("adopt: %s: %v", d.Name, err)
				continue
			}
			owned := s.ownedJails(be, d.Name)
			for _, u := range list {
				if owned[u.Name] {
					continue
				}
				if u.Project != "" {
					if _, err := s.manager.Get(u.Project); err == nil {
						continue // a fjord stack's own director project
					}
				}
				c := adoptCandidate{Unmanaged: u, Engine: d.Name}
				if a, err := adoptSpec(u); err != nil {
					c.Error = err.Error()
				} else {
					c.Service, c.Compose, c.Env, c.Notes = a.Service, a.Compose, a.Env, a.Notes
					c.Director, c.Makejail, c.Template = a.Director, a.Makejail, a.Template
				}
				out = append(out, c)
			}
		}
		if out == nil {
			out = []adoptCandidate{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)

	case http.MethodPost:
		var req struct {
			Container string `json:"container"`
			Engine    string `json:"engine"`
			Name      string `json:"name"`
			Replace   bool   `json:"replace"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Container == "" {
			http.Error(w, "container required", 400)
			return
		}
		if req.Engine == "" {
			req.Engine = s.defaultEngine()
		}
		be, ok := s.backend(req.Engine)
		if !ok {
			http.Error(w, "engine not available: "+req.Engine, 400)
			return
		}
		ad, ok := be.(adopter)
		if !ok {
			http.Error(w, "engine "+req.Engine+" can't adopt containers", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		list, err := ad.UnmanagedContainers(ctx)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		var found *engine.Unmanaged
		for i := range list {
			if list[i].Name == req.Container || list[i].ID == req.Container {
				found = &list[i]
				break
			}
		}
		if found == nil {
			http.Error(w, "no unmanaged container "+req.Container, 404)
			return
		}
		a, err := adoptSpec(*found)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = found.Name
		}
		id, release := s.manager.AllocateName(name, a.Service)
		defer release()
		compose := a.Compose
		if port, https := s.adoptWebHint(ctx, be, a.Service, found.Image); port != "" {
			compose += fmt.Sprintf("\nx-fjord:\n  web_port: %q\n", port)
			if https {
				compose += "  web_https: true\n"
			}
		}
		env := a.Env
		if a.Director != "" {
			// director keys its per-project state by this; the stack id keeps
			// it unique, as catalog installs do
			env = "DIRECTOR_PROJECT=" + id + "\n" + env
		}
		st := &stack.Stack{Name: id, Compose: compose, Env: env, Director: a.Director, Makejail: a.Makejail}
		if err := s.manager.Save(st); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if a.Template != "" {
			if err := os.WriteFile(filepath.Join(st.Dir, "template.conf"), []byte(strings.TrimRight(a.Template, "\n")+"\n"), 0o644); err != nil {
				http.Error(w, "template.conf: "+err.Error(), 500)
				return
			}
		}
		st.State = &stack.State{
			SchemaVersion: 1,
			Origin:        stack.OriginInfo{Type: "adopted", AppID: a.Service},
			DesiredState:  "stopped",
			Engine:        req.Engine,
			DisplayName:   name,
			InstalledAt:   time.Now().UTC().Format(time.RFC3339),
			UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		}
		if err := s.manager.SaveState(id, st.State); err != nil {
			log.Printf("adopt %s: persist state: %v", id, err)
		}
		copyStackIcon(st.Dir, s.cat.AppIconPath(a.Service))
		log.Printf("adopt %s <- container %s (%s)", id, found.Name, req.Engine)
		if req.Replace {
			if err := ad.RemoveContainer(ctx, found.ID); err != nil {
				http.Error(w, "stack "+id+" created, but removing the old container failed: "+err.Error(), 502)
				return
			}
			log.Printf("adopt %s: removed old container %s", id, found.Name)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": id, "service": a.Service, "notes": a.Notes, "replaced": req.Replace})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// adoptWebHint finds the port the app's web UI answers on, for the Open link
// of an adopted stack (which publishes nothing on a macvlan): the catalog
// manifest's web_port/web_https when the image is a catalog app, else the
// image's first exposed TCP port. "" = no hint.
func (s *server) adoptWebHint(ctx context.Context, be engine.Backend, appID, image string) (port string, https bool) {
	if b, err := s.cat.ManifestAny(ctx, appID); err == nil {
		if m, err := manifest.Parse(string(b)); err == nil {
			if p := m.WebContainerPort(); p != "" {
				return p, m.WebHTTPS
			}
		}
	}
	if ex, ok := be.(interface {
		ImageExposedPorts(context.Context, string) ([]string, error)
	}); ok {
		if keys, err := ex.ImageExposedPorts(ctx, image); err == nil {
			for _, k := range keys {
				if p, proto, _ := strings.Cut(k, "/"); proto == "" || proto == "tcp" {
					return p, p == "443" || p == "8443"
				}
			}
		}
	}
	return "", false
}
