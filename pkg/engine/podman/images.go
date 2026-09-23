package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// ImageRepoDigests inspects a locally-present image via the libpod REST API and
// returns its RepoDigests ("repo@sha256:..."). An image that isn't pulled yields
// an empty slice (404), not an error, so update detection can report "unknown".
func (b *Backend) ImageRepoDigests(ctx context.Context, ref string) ([]string, error) {
	u := "http://d/v4.0.0/libpod/images/" + url.PathEscape(ref) + "/json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("libpod images inspect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // image not present locally
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("libpod images inspect: unexpected status %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out struct {
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode image inspect: %w", err)
	}
	return out.RepoDigests, nil
}

// ImageExposedPorts returns a locally-present image's EXPOSE entries as
// "port/proto" keys ("5432/tcp"). Not pulled yet -> nil, nil. Pre-flight uses
// it for network_mode: host services, whose ports never appear in `ports:`.
func (b *Backend) ImageExposedPorts(ctx context.Context, ref string) ([]string, error) {
	u := "http://d/v4.0.0/libpod/images/" + url.PathEscape(ref) + "/json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("libpod images inspect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("libpod images inspect: unexpected status %s", resp.Status)
	}
	var out struct {
		Config struct {
			ExposedPorts map[string]struct{} `json:"ExposedPorts"`
		} `json:"Config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode image inspect: %w", err)
	}
	keys := make([]string, 0, len(out.Config.ExposedPorts))
	for k := range out.Config.ExposedPorts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// RunningImages reports the image each of the stack's containers was created
// from, keyed to its compose service by podman-compose's service label.
//
// The digest comes from the CONTAINER (ImageDigest), not from the image. A
// pull that moves a tag strips the old image's RepoDigests entirely, so asking
// the image what it was pulled as stops working exactly when it matters.
func (b *Backend) RunningImages(ctx context.Context, s *stack.Stack) ([]engine.RunningImage, error) {
	cs, err := b.listStackContainers(ctx, s.Name)
	if err != nil {
		return nil, err
	}
	var out []engine.RunningImage
	seen := map[string]bool{}
	for _, c := range cs {
		svc := c.Labels["io.podman.compose.service"]
		if svc == "" || seen[svc] {
			continue // one container per service answers for it
		}
		id, ref, digest, err := b.containerImage(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		seen[svc] = true
		digests := []string{}
		if digest != "" {
			digests = append(digests, digest)
		}
		if repo, err := b.ImageRepoDigests(ctx, id); err == nil {
			for _, d := range repo {
				if at := strings.LastIndex(d, "@"); at >= 0 && d[at+1:] != digest {
					digests = append(digests, d[at+1:])
				}
			}
		}
		out = append(out, engine.RunningImage{Service: svc, ImageID: id, Ref: ref, Digest: digest, Digests: digests})
	}
	return out, nil
}

// containerImage is a container's image ID, the ref it was created from, and
// the digest that ref was pulled as.
func (b *Backend) containerImage(ctx context.Context, id string) (imageID, ref, digest string, err error) {
	u := "http://d/v4.0.0/libpod/containers/" + url.PathEscape(id) + "/json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", "", err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("libpod containers inspect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", "", "", nil // removed between list and inspect
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("libpod containers inspect: unexpected status %s", resp.Status)
	}
	var out struct {
		Image       string `json:"Image"`
		ImageName   string `json:"ImageName"`
		ImageDigest string `json:"ImageDigest"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", "", fmt.Errorf("decode container inspect: %w", err)
	}
	return out.Image, out.ImageName, out.ImageDigest, nil
}
