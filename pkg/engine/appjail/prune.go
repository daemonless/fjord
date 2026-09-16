package appjail

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/daemonless/fjord/pkg/engine"
)

// Storage on the appjail engine is buildah's: OCI images plus the working
// containers builds leave behind. Jails are never touched here (they are the
// stacks), and appjail has no volumes or networks of its own to prune.

func (b *Backend) PruneCapabilities() engine.PruneCapabilities {
	return engine.PruneCapabilities{Images: true, Build: true}
}

// Capabilities: appjail has no remote-volume support -- FreeBSD's smbfs is
// SMB1-only and the OCI path doesn't mount nfs:// / smb:// folders.
func (b *Backend) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		RemoteVolumes: false, NetworkKinds: networkKinds(),
		NetworkNote: "Jails attach to networks but this engine does not create them — switch to podman to add one.",
	}
}

// StoreSMBCredentials is unsupported on appjail (see Capabilities).
func (b *Backend) StoreSMBCredentials(server, username, password string) error {
	return fmt.Errorf("the appjail engine does not support SMB volumes")
}

// buildah prints sizes as humanized strings ("730 MB"); parse them back to bytes.
type buildahImage struct {
	ID    string   `json:"id"`
	Names []string `json:"names"`
	Size  string   `json:"size"`
}

func (im buildahImage) bytes() int64 { return parseHumanBytes(im.Size) }

var unitBytes = map[string]float64{"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12, "kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40}

// parseHumanBytes reads "730 MB", "1.2GB", "512 B", "3.5 GiB"; 0 when unparseable.
func parseHumanBytes(s string) int64 {
	s = strings.TrimSpace(strings.ToLower(s))
	i := strings.LastIndexAny(s, "0123456789.")
	if i < 0 {
		return 0
	}
	num, unit := strings.TrimSpace(s[:i+1]), strings.TrimSpace(s[i+1:])
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	mult, ok := unitBytes[unit]
	if !ok {
		return 0
	}
	return int64(f * mult)
}

func (b *Backend) DiskUsage(ctx context.Context) ([]engine.DiskRow, error) {
	out, err := exec.CommandContext(ctx, "buildah", "images", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("buildah images: %w", err)
	}
	var images []buildahImage
	if err := json.Unmarshal(out, &images); err != nil {
		return nil, fmt.Errorf("parse buildah images: %w", err)
	}
	rows := []engine.DiskRow{imageRow(images)}

	ctrs := buildahContainers(ctx)
	leftovers := 0
	for _, c := range ctrs {
		if !c.jailBacked() {
			leftovers++
		}
	}
	rows = append(rows, engine.DiskRow{
		Type: "Build containers", Total: len(ctrs), Active: len(ctrs) - leftovers,
		Size: "-", Reclaimable: fmt.Sprintf("%d", leftovers),
		RawReclaimable: int64(leftovers),
		Note:           "appjail-* containers are the image source of a jail and stay; only *-working-container build leftovers are reclaimable",
	})
	return rows, nil
}

// buildahContainer is one row of `buildah containers --json`.
type buildahContainer struct {
	ID            string `json:"id"`
	ContainerName string `json:"containername"`
	ImageName     string `json:"imagename"`
}

// jailBacked reports whether a buildah container is an appjail OCI jail's
// image source (appjail names them "appjail-<jail>"). Removing one takes the
// running jail's root filesystem with it, so prune must never touch these.
func (c buildahContainer) jailBacked() bool {
	return strings.HasPrefix(c.ContainerName, "appjail-")
}

func buildahContainers(ctx context.Context) []buildahContainer {
	out, err := exec.CommandContext(ctx, "buildah", "containers", "--json").Output()
	if err != nil {
		return nil
	}
	var ctrs []buildahContainer
	_ = json.Unmarshal(out, &ctrs)
	return ctrs
}

// imageRow sums buildah images; untagged (dangling) ones are the reclaimable part.
func imageRow(images []buildahImage) engine.DiskRow {
	var total, dangling int64
	var n, nd int
	for _, im := range images {
		n++
		sz := im.bytes()
		total += sz
		if len(im.Names) == 0 {
			nd++
			dangling += sz
		}
	}
	pct := ""
	if total > 0 {
		pct = fmt.Sprintf(" (%d%%)", dangling*100/total)
	}
	return engine.DiskRow{
		Type: "Images", Total: n, Active: n - nd,
		Size: engine.HumanBytes(total), Reclaimable: engine.HumanBytes(dangling) + pct, RawReclaimable: dangling,
	}
}

func (b *Backend) Prune(ctx context.Context, opts engine.PruneOptions) (engine.PruneReport, error) {
	var out strings.Builder
	run := func(args ...string) {
		fmt.Fprintf(&out, "$ buildah %s\n", strings.Join(args, " "))
		o, err := exec.CommandContext(ctx, "buildah", args...).CombinedOutput()
		out.Write(o)
		if err != nil {
			fmt.Fprintf(&out, "[warn] %v\n", err)
		}
		out.WriteString("\n")
	}
	var before int64
	if rows, err := b.DiskUsage(ctx); err == nil && len(rows) > 0 {
		before = rows[0].RawReclaimable
	}
	if opts.Build {
		// Never `rm --all`: that includes the appjail-* containers backing
		// running jails. Remove only genuine build leftovers, one by one.
		removed := 0
		for _, c := range buildahContainers(ctx) {
			if c.jailBacked() {
				continue
			}
			run("rm", c.ID)
			removed++
		}
		if removed == 0 {
			out.WriteString("no build leftovers to remove (appjail-* containers belong to jails and are kept)\n\n")
		}
	}
	if opts.Images || opts.AllImages {
		run("rmi", "--prune") // untagged only; tagged images stay for the jails that reference them
	}
	reclaimed := ""
	if before > 0 {
		reclaimed = engine.HumanBytes(before)
	}
	return engine.PruneReport{Reclaimed: reclaimed, Output: out.String()}, nil
}
