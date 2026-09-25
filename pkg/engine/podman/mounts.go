package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
)

// HostMounts is every host path mounted into any container podman knows --
// fjord's or not, running or stopped. What left-over app data must be
// checked against: a folder no fjord stack uses can still be the live data of
// a container someone runs by hand or from Ansible (jupiter's /containers).
func (b *Backend) HostMounts(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://d/v4.0.0/libpod/containers/json?all=true", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("libpod containers/json: unexpected status %s", resp.Status)
	}
	var list []libpodContainer
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	var out []string
	for _, c := range list {
		paths, err := b.containerMounts(ctx, c.ID)
		if err != nil {
			// One container that cannot be read makes the whole answer
			// incomplete, and an incomplete answer here means deleting data.
			return nil, err
		}
		out = append(out, paths...)
	}
	return out, nil
}

// containerMounts is the host side of each of a container's mounts. The
// type is not checked: on FreeBSD a bind is "nullfs", not "bind".
func (b *Backend) containerMounts(ctx context.Context, id string) ([]string, error) {
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
		return nil, fmt.Errorf("inspect %s: %s", id, resp.Status)
	}
	var c struct {
		Mounts []struct{ Source string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range c.Mounts {
		if filepath.IsAbs(m.Source) {
			out = append(out, filepath.Clean(m.Source))
		}
	}
	return out, nil
}
