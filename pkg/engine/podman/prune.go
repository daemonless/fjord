package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/daemonless/fjord/pkg/engine"
)

// DiskUsage builds the df report from the cheap listings. `podman system df`
// (CLI and API alike) walks every layer and container to size them and takes
// 10-30 s on a host with a few dozen images -- switching engines on the
// System page hung on it -- while images/ps/volume ls answer in well under a
// second. Image bytes are exact (per-image Size; "unused" = no container on
// it, which is what `image prune -a` reclaims). Containers and volumes are
// reported as counts: sizing them is the slow part.
func (b *Backend) DiskUsage(ctx context.Context) ([]engine.DiskRow, error) {
	var (
		imgs []struct {
			Size       int64
			Containers int
		}
		ctrs     []psEntry
		vols     []struct{ Name string }
		dangling []struct{ Name string }
		imgErr   error
		wg       sync.WaitGroup
	)
	wg.Add(4)
	go func() { defer wg.Done(); imgErr = listJSON(ctx, &imgs, "images", "--format", "json") }()
	go func() { defer wg.Done(); _ = listJSON(ctx, &ctrs, "ps", "-a", "--external", "--format", "json") }()
	go func() { defer wg.Done(); _ = listJSON(ctx, &vols, "volume", "ls", "--format", "json") }()
	go func() {
		defer wg.Done()
		_ = listJSON(ctx, &dangling, "volume", "ls", "--filter", "dangling=true", "--format", "json")
	}()
	wg.Wait()
	if imgErr != nil {
		return nil, fmt.Errorf("podman images: %w", imgErr)
	}

	var total, unused int64
	active := 0
	for _, im := range imgs {
		total += im.Size
		if im.Containers > 0 {
			active++
		} else {
			unused += im.Size
		}
	}
	pct := ""
	if total > 0 {
		pct = fmt.Sprintf(" (%d%%)", unused*100/total)
	}
	images := engine.DiskRow{
		Type: "Images", Total: len(imgs), Active: active,
		Size: engine.HumanBytes(total), Reclaimable: engine.HumanBytes(unused) + pct, RawReclaimable: unused,
	}
	if names := externalNames(ctrs); len(names) > 0 {
		shown := names
		more := ""
		if len(shown) > 4 {
			shown, more = names[:4], fmt.Sprintf(" and %d more", len(names)-4)
		}
		images.Note = fmt.Sprintf("Held by %d external container%s, which prune leaves alone: %s%s (appjail-* are appjail jails' image sources; *-working-container are buildah builds in progress or leftovers)",
			len(names), map[bool]string{true: "", false: "s"}[len(names) == 1], strings.Join(shown, ", "), more)
	}

	owned, running := 0, 0
	for _, c := range ctrs {
		if strings.EqualFold(c.State, "storage") {
			continue
		}
		owned++
		if strings.EqualFold(c.State, "running") {
			running++
		}
	}
	containers := engine.DiskRow{
		Type: "Containers", Total: owned, Active: running,
		Size: "-", Reclaimable: fmt.Sprintf("%d stopped", owned-running), RawReclaimable: int64(owned - running),
	}
	volumes := engine.DiskRow{
		Type: "Local Volumes", Total: len(vols), Active: len(vols) - len(dangling),
		Size: "-", Reclaimable: fmt.Sprintf("%d unused", len(dangling)), RawReclaimable: int64(len(dangling)),
	}
	return []engine.DiskRow{images, containers, volumes}, nil
}

// listJSON runs `podman <args>` and decodes its JSON output into v.
func listJSON(ctx context.Context, v any, args ...string) error {
	out, err := exec.CommandContext(ctx, "podman", args...).Output()
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

// psEntry is the slice of `podman ps --format json` DiskUsage needs.
type psEntry struct {
	Names []string
	ID    string `json:"Id"`
	State string
}

// externalNames names the storage containers podman doesn't own (state
// "storage": buildah working containers, appjail's OCI jails, orphaned
// records). Their images count as active, and none of the prune commands
// touch them.
func externalNames(list []psEntry) []string {
	var names []string
	for _, c := range list {
		if !strings.EqualFold(c.State, "storage") {
			continue
		}
		name := c.ID
		if len(name) > 12 {
			name = name[:12]
		}
		if len(c.Names) > 0 && c.Names[0] != "" {
			name = c.Names[0]
		}
		names = append(names, name)
	}
	return names
}

var reclaimedRe = regexp.MustCompile(`(?i)Total reclaimed space:\s*(.+)`)

// PruneCapabilities: podman can prune every category.
// Capabilities: podman mounts nfs:// / smb:// folders as named volumes.
func (b *Backend) Capabilities() engine.Capabilities {
	return engine.Capabilities{RemoteVolumes: true, NetworkKinds: b.networkKinds()}
}

func (b *Backend) PruneCapabilities() engine.PruneCapabilities {
	return engine.PruneCapabilities{Containers: true, Images: true, AllImages: true, Volumes: true, Networks: true, Build: true}
}

// Prune reclaims space with the podman CLI (one pass per selected category, so a
// failure in one still lets the others run). The per-command output is captured
// and the last "Total reclaimed space" line surfaced as the headline figure.
func (b *Backend) Prune(ctx context.Context, opts engine.PruneOptions) (engine.PruneReport, error) {
	var out strings.Builder
	run := func(args ...string) {
		fmt.Fprintf(&out, "$ podman %s\n", strings.Join(args, " "))
		o, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
		out.Write(o)
		if err != nil {
			fmt.Fprintf(&out, "[warn] %v\n", err)
		}
		out.WriteString("\n")
	}
	if opts.Containers {
		run("container", "prune", "-f")
	}
	if opts.Images || opts.AllImages {
		if opts.AllImages {
			run("image", "prune", "-a", "-f")
		} else {
			run("image", "prune", "-f")
		}
	}
	if opts.Networks {
		run("network", "prune", "-f")
	}
	if opts.Build {
		// The only CLI path to buildah working containers + build cache. It also
		// removes stopped containers, dangling images and unused networks, which
		// is why the UI ties those on when Build is picked.
		run("system", "prune", "--build", "-f")
	}
	if opts.Volumes {
		run("volume", "prune", "-f")
	}

	// Sum is hard across passes; surface the last reported total as the headline.
	reclaimed := ""
	for _, m := range reclaimedRe.FindAllStringSubmatch(out.String(), -1) {
		if v := strings.TrimSpace(m[1]); v != "" && v != "0B" {
			reclaimed = v
		}
	}
	return engine.PruneReport{Reclaimed: reclaimed, Output: out.String()}, nil
}
