package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
)

// UnmanagedContainers lists containers that belong to no compose project --
// started by hand, Ansible, `podman run` -- with the argv podman recorded
// for each (.Config.CreateCommand). On FreeBSD inspect reports no IP or
// MAC for a macvlan container, so that run line is the only faithful
// source of its network address. Storage-only records are skipped.
func (b *Backend) UnmanagedContainers(ctx context.Context) ([]engine.Unmanaged, error) {
	q := url.Values{}
	q.Set("all", "true")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/containers/json?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("libpod containers/json: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("libpod containers/json: unexpected status %s", resp.Status)
	}
	var list []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Image  string            `json:"Image"`
		State  string            `json:"State"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decode containers: %w", err)
	}
	var out []engine.Unmanaged
	projects := map[string]*engine.Unmanaged{}
	var order []string
	for _, c := range list {
		if p := c.Labels["io.podman.compose.project"]; p != "" || c.Labels["com.docker.compose.project"] != "" {
			// One candidate per compose project, imported from its own file.
			// Skipped one by one, Plex (podman-compose under systemd) was
			// simply missing from the list.
			if p == "" {
				p = c.Labels["com.docker.compose.project"]
			}
			u := projects[p]
			if u == nil {
				u = &engine.Unmanaged{ID: "project:" + p, Name: p, Project: p, Image: c.Image, State: c.State,
					WorkDir: c.Labels["com.docker.compose.project.working_dir"], Unit: c.Labels["PODMAN_SYSTEMD_UNIT"]}
				for _, f := range strings.Split(c.Labels["com.docker.compose.project.config_files"], ",") {
					if f = strings.TrimSpace(f); f != "" {
						u.ComposeFiles = append(u.ComposeFiles, f)
					}
				}
				projects[p] = u
				order = append(order, p)
			}
			if svc := c.Labels["io.podman.compose.service"]; svc != "" {
				u.Services = append(u.Services, svc)
			}
			if c.State == "running" {
				u.State = "running"
			}
			continue
		}
		if len(c.Names) == 0 {
			continue
		}
		u := engine.Unmanaged{ID: c.ID, Name: c.Names[0], Image: c.Image, State: c.State}
		u.RunArgs, _ = b.createCommand(ctx, c.ID) // best-effort
		if len(u.RunArgs) == 0 {
			u.Unadoptable = "podman kept no run command for this container"
		}
		out = append(out, u)
	}
	for _, p := range order {
		u := projects[p]
		switch {
		case u.WorkDir == "" || len(u.ComposeFiles) == 0:
			u.Unadoptable = "podman did not record where this project's compose file is"
		case len(u.ComposeFiles) > 1:
			u.Unadoptable = "made from several compose files (" + strings.Join(u.ComposeFiles, ", ") + "); import takes one"
		}
		out = append(out, *u)
	}
	return out, nil
}

// createCommand returns the argv a container was created with.
func (b *Backend) createCommand(ctx context.Context, id string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/containers/"+url.PathEscape(id)+"/json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("inspect: %s", resp.Status)
	}
	var out struct {
		Config struct {
			CreateCommand []string `json:"CreateCommand"`
		} `json:"Config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Config.CreateCommand, nil
}

// RemoveContainer stops and removes one container by name or id -- the old
// copy of an adopted service, so the stack can take its name.
func (b *Backend) RemoveContainer(ctx context.Context, name string) error {
	if out, err := exec.CommandContext(ctx, "podman", "rm", "-f", name).CombinedOutput(); err != nil {
		return fmt.Errorf("podman rm -f %s: %v: %s", name, err, string(out))
	}
	return nil
}
