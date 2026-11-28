package dataset

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// KindFreshInstall marks a kept copy set aside by an install that started
// over instead of using the data an earlier install left.
const KindFreshInstall = "fresh-install"

// LiveDataset is a dataset fjord made for a stack that is not a kept copy.
type LiveDataset struct {
	Name       string
	Stack      string
	Mountpoint string // "" when it has none
	UsedBytes  int64
}

// ListLive returns every live dataset carrying fjord's stack tag, kept copies
// left out. No zfs or no pool is an error the caller may ignore.
func ListLive(ctx context.Context) ([]LiveDataset, error) {
	out, err := run(ctx, "list", "-H", "-p", "-o", "name,mountpoint,used,"+Tag+","+PropKeptKind, "-t", "filesystem")
	if err != nil {
		return nil, err
	}
	var res []LiveDataset
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 5 || f[3] == "" || f[3] == "-" {
			continue
		}
		if f[4] != "" && f[4] != "-" {
			continue // a kept copy
		}
		mp := f[1]
		if mp == "none" || mp == "legacy" || mp == "-" {
			mp = ""
		}
		used, _ := strconv.ParseInt(f[2], 10, 64)
		res = append(res, LiveDataset{Name: f[0], Stack: f[3], Mountpoint: mp, UsedBytes: used})
	}
	return res, nil
}

// SetAside turns liveName into a read-only kept copy of stack that expires
// after ttl, leaving no live dataset behind: the caller starts over with an
// empty one. Its snapshots go with it. Returns the kept copy's name.
func SetAside(ctx context.Context, liveName, stack, kind, version string, now time.Time, ttl time.Duration) (string, error) {
	if err := Owned(ctx, liveName, stack); err != nil {
		return "", err
	}
	lp, err := LocalProperties(ctx, liveName)
	if err != nil {
		return "", err
	}
	suffix := ".kept-" + now.UTC().Format("2006-01-02-150405")
	keptName := liveName + suffix
	if _, err := run(ctx, "rename", liveName, keptName); err != nil {
		return "", fmt.Errorf("zfs rename %s -> %s: %w", liveName, keptName, err)
	}
	if lp.LocalMP != "" {
		if _, err := run(ctx, "set", "mountpoint="+lp.LocalMP+suffix, keptName); err != nil {
			_, _ = run(ctx, "rename", "-f", keptName, liveName)
			return "", fmt.Errorf("zfs set mountpoint on %s: %w", keptName, err)
		}
	}
	setKeptProps(ctx, keptName, stack, kind, version, "", now, ttl)
	return keptName, nil
}
