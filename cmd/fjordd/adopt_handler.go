package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/registry"
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

// importSpec is a compose project started outside fjord, as the stack it
// becomes: its own compose file, with paths relative to its folder made
// absolute (fjord keeps the copy in its stack folder), and its .env. The
// original files stay where they are.
func importSpec(u engine.Unmanaged) (*engine.AdoptSpec, error) {
	file := u.ComposeFiles[0]
	if !filepath.IsAbs(file) {
		file = filepath.Join(u.WorkDir, file)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read the project's compose file: %w", err)
	}
	compose, err := composepkg.AbsolutePaths(string(raw), u.WorkDir)
	if err != nil {
		return nil, err
	}
	a := &engine.AdoptSpec{Service: u.Project, Compose: compose}
	if env, err := os.ReadFile(filepath.Join(u.WorkDir, ".env")); err == nil {
		a.Env = string(env)
	}
	a.Notes = append(a.Notes, "Imported from "+file+"; that file stays where it is, fjord keeps its own copy.")
	if unitEnabled(u.Unit) {
		a.Notes = append(a.Notes, "The systemd unit "+u.Unit+" also runs this project: turn it off (systemctl disable --now "+u.Unit+"), or both will start it.")
	}
	return a, nil
}

// unitEnabled reports whether a systemd unit exists and is enabled. podman-
// compose labels every project with podman-compose@<project>.service whether
// or not that unit was ever installed, so the label alone says nothing.
func unitEnabled(unit string) bool {
	if unit == "" {
		return false
	}
	out, err := exec.Command("systemctl", "is-enabled", unit).Output()
	return err == nil && strings.TrimSpace(string(out)) == "enabled"
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
	if len(u.ComposeFiles) > 0 {
		return importSpec(u)
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
		project := len(found.ComposeFiles) > 0
		if project && req.Replace {
			http.Error(w, "a compose project is imported as it runs; there is nothing to replace", 400)
			return
		}
		id, release := s.manager.AllocateName(name, a.Service)
		if project {
			// Its containers carry the project's name: the stack must have it
			// too, or fjord would not know them as its own.
			// As recorded, not slugged: "my_app" must stay "my_app".
			release()
			id, release = found.Project, func() {}
			if !stack.ValidName(id) {
				http.Error(w, "the project's name "+id+" cannot be a stack name", 409)
				return
			}
			if _, err := s.manager.Get(id); err == nil {
				http.Error(w, "a stack named "+id+" already exists; the project keeps its name", 409)
				return
			}
		}
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
			DesiredState:  map[bool]string{true: "running", false: "stopped"}[project && found.State == "running"],
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
// manifest's web_port/web_https when the image is that catalog app's, else
// the image's first exposed TCP port. "" = no hint.
func (s *server) adoptWebHint(ctx context.Context, be engine.Backend, appID, image string) (port string, https bool) {
	if b, err := s.cat.ManifestAny(ctx, appID); err == nil {
		if m, err := manifest.Parse(string(b)); err == nil && runsImage(m.Compose(), image) {
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

// runsImage: the compose runs the image's repository. An app found by name
// can be another image of the same app -- plexinc/pms-docker is not
// linuxserver's plex, whose web port it was given (venus, 2026-10-29).
func runsImage(compose, image string) bool {
	imgs, _ := composepkg.ServiceImages(compose)
	for _, i := range imgs {
		if sameRepo(i, image) {
			return true
		}
	}
	return false
}

// sameRepo compares two image refs by repository: tag and digest dropped,
// Docker Hub spelled either way.
func sameRepo(a, b string) bool {
	norm := func(s string) string {
		r := registry.Repo(s)
		r = strings.TrimPrefix(r, "docker.io/")
		return strings.TrimPrefix(r, "library/")
	}
	return norm(a) == norm(b)
}
