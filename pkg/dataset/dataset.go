// Package dataset gives an app its own ZFS dataset when its data location
// is a dataset's mountpoint: <parent>/<stack>, every property inherited,
// tagged so fjord can prove it made it before it ever destroys it.
package dataset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tag is the user property fjord sets on a dataset it made: its stack.
const Tag = "fjord:stack"

// Snapshot name prefixes. Automatic update snapshots use UpdateSnapPrefix
// (and legacy LegacySnapPrefix followed strictly by digits). Scheduled daily
// restore points use ScheduledSnapPrefix. Manual restore points use
// ManualSnapPrefix so automatic retention never touches them.
const (
	UpdateSnapPrefix    = "fjord-update-"
	LegacySnapPrefix    = "fjord-"
	ScheduledSnapPrefix = "fjord-scheduled-"
	ManualSnapPrefix    = "fjord-manual-"
)

// User properties stored on kept-copy datasets created during restore or
// dataset migration. Live-dataset lookups skip any dataset with PropKeptKind
// set. PropNote stores an optional user note on a manual snapshot.
const (
	PropKeptKind    = "fjord:kept-kind"
	PropKeptAt      = "fjord:kept-at"
	PropKeptUntil   = "fjord:kept-until"
	PropKeptVersion = "fjord:kept-version"
	PropKeptImage   = "fjord:kept-image"
	PropNote        = "fjord:note"
	PropImages      = "fjord:images"
	PropKeep        = "fjord:keep"
)

// SnapInfo describes one fjord-managed ZFS snapshot.
type SnapInfo struct {
	Name         string
	Kind         string // "update", "scheduled", or "manual"
	Note         string
	Images       string // JSON map of service -> ref@digest captured when taken
	Created      time.Time
	UsedBytes    int64
	DeferDestroy bool
	KeepForever  bool
	Clones       []string
}

// run is the zfs command; tests replace it.
var run = func(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "zfs", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, errors.New(msg)
		}
	}
	return out, err
}

// At names the dataset mounted exactly at dir, if there is one. No zfs, no
// pool, or a plain folder: ok is false.
func At(ctx context.Context, dir string) (name string, ok bool) {
	dir = filepath.Clean(dir)
	out, err := run(ctx, "list", "-H", "-o", "name,mountpoint", "-t", "filesystem")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 2 && filepath.Clean(f[1]) == dir {
			return f[0], true
		}
	}
	return "", false
}

// LiveByTag finds the live dataset tagged for stack, ignoring any kept-copy
// datasets (which also carry fjord:stack for ownership checks, plus
// fjord:kept-kind).
func LiveByTag(ctx context.Context, stack string) (name string, ok bool) {
	if stack == "" {
		return "", false
	}
	out, err := run(ctx, "list", "-H", "-o", "name,"+Tag+","+PropKeptKind, "-t", "filesystem")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		if f[1] == stack && (f[2] == "-" || f[2] == "") {
			return f[0], true
		}
	}
	return "", false
}

// Create makes <parent>/<stack>, mounted under the parent's mountpoint by
// inheritance, tagged with the stack.
func Create(ctx context.Context, parent, stack string) (string, error) {
	if stack == "" || strings.ContainsAny(stack, "/@# ") {
		return "", fmt.Errorf("not a dataset name: %q", stack)
	}
	name := parent + "/" + stack
	if _, err := run(ctx, "create", "-o", Tag+"="+stack, name); err != nil {
		return "", fmt.Errorf("zfs create %s: %w", name, err)
	}
	return name, nil
}

// validDatasetName rejects empty strings, leading dashes (which zfs would
// parse as CLI flags), snapshot/bookmark separators, whitespace, and control
// characters so untrusted API parameters can never inject zfs options.
func validDatasetName(name string) bool {
	if name == "" || name[0] == '-' || name[0] == '/' || strings.HasSuffix(name, "/") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c <= 0x20 || c >= 0x7f || c == '@' || c == '#' {
			return false
		}
	}
	return true
}

// Owned says whether name and every dataset under it carry stack's tag. A
// child fjord did not make is named in the error, so it is never destroyed
// along with the app.
func Owned(ctx context.Context, name, stack string) error {
	if !validDatasetName(name) {
		return fmt.Errorf("invalid dataset name %q", name)
	}
	out, err := run(ctx, "list", "-H", "-r", "-o", "name,"+Tag, "-t", "filesystem,volume", name)
	if err != nil {
		return fmt.Errorf("zfs list %s: %w", name, err)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 2 {
			continue
		}
		if f[1] != stack {
			if f[0] == name {
				return fmt.Errorf("%s wasn't made by fjord for %s", name, stack)
			}
			return fmt.Errorf("%s wasn't made by fjord; remove it first", f[0])
		}
		found = found || f[0] == name
	}
	if !found {
		return fmt.Errorf("%s not found", name)
	}
	return nil
}

// Destroy removes name with its snapshots and children, only when Owned
// says all of it is stack's.
func Destroy(ctx context.Context, name, stack string) error {
	if err := Owned(ctx, name, stack); err != nil {
		return err
	}
	if _, err := run(ctx, "destroy", "-r", name); err != nil {
		return fmt.Errorf("zfs destroy %s: %w", name, err)
	}
	return nil
}

// IsInternalDir reports whether a directory entry under an App data location
// is a fjord-managed kept copy, pre-dataset safety folder, or in-flight
// deletion folder that orphan and adoption scans must skip.
func IsInternalDir(name string) bool {
	return strings.Contains(name, ".kept-") ||
		strings.Contains(name, ".pre-dataset-") ||
		strings.Contains(name, ".deleting-")
}

// IsUpdateSnap reports whether fullSnap is one of fjord's automatic
// pre-update snapshots of datasetName (<name>@fjord-update-<digits> or legacy
// <name>@fjord-<digits>).
func IsUpdateSnap(datasetName, fullSnap string) bool {
	rest, ok := strings.CutPrefix(fullSnap, datasetName+"@")
	if !ok {
		return false
	}
	if tail, ok := strings.CutPrefix(rest, UpdateSnapPrefix); ok {
		return allDigits(tail)
	}
	if tail, ok := strings.CutPrefix(rest, LegacySnapPrefix); ok {
		return allDigits(tail)
	}
	return false
}

// IsManualSnap reports whether fullSnap is one of fjord's manual restore
// points of datasetName (<name>@fjord-manual-<digits>).
func IsManualSnap(datasetName, fullSnap string) bool {
	rest, ok := strings.CutPrefix(fullSnap, datasetName+"@")
	if !ok {
		return false
	}
	tail, ok := strings.CutPrefix(rest, ManualSnapPrefix)
	return ok && allDigits(tail)
}

// IsScheduledSnap reports whether fullSnap is one of fjord's scheduled daily
// restore points of datasetName (<name>@fjord-scheduled-<digits>).
func IsScheduledSnap(datasetName, fullSnap string) bool {
	rest, ok := strings.CutPrefix(fullSnap, datasetName+"@")
	if !ok {
		return false
	}
	tail, ok := strings.CutPrefix(rest, ScheduledSnapPrefix)
	return ok && allDigits(tail)
}

// IsFjordSnap reports whether fullSnap is any fjord-managed snapshot of
// datasetName.
func IsFjordSnap(datasetName, fullSnap string) bool {
	return IsUpdateSnap(datasetName, fullSnap) ||
		IsScheduledSnap(datasetName, fullSnap) ||
		IsManualSnap(datasetName, fullSnap)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ListSnapshots returns fjord-managed snapshots of name in creation order.
// When includeDeferred is false, snapshots already marked defer_destroy=on
// are omitted so UI lists do not show deleted-but-clone-held snapshots.
func ListSnapshots(ctx context.Context, name string, includeDeferred bool) ([]SnapInfo, error) {
	out, err := run(ctx, "list", "-H", "-p", "-t", "snapshot",
		"-o", "name,creation,used,defer_destroy,clones,"+PropNote+","+PropImages+","+PropKeep,
		"-s", "creation", "-d", "1", name)
	if err != nil {
		return nil, fmt.Errorf("zfs list snapshots %s: %w", name, err)
	}
	var snaps []SnapInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 5 {
			continue
		}
		kind := ""
		switch {
		case IsUpdateSnap(name, f[0]):
			kind = "update"
		case IsScheduledSnap(name, f[0]):
			kind = "scheduled"
		case IsManualSnap(name, f[0]):
			kind = "manual"
		default:
			continue
		}
		deferred := f[3] == "on"
		if deferred && !includeDeferred {
			continue
		}
		sec, _ := strconv.ParseInt(f[1], 10, 64)
		used, _ := strconv.ParseInt(f[2], 10, 64)
		var clones []string
		if f[4] != "" && f[4] != "-" {
			for _, c := range strings.Split(f[4], ",") {
				if c = strings.TrimSpace(c); c != "" {
					clones = append(clones, c)
				}
			}
		}
		note := ""
		if len(f) >= 6 && f[5] != "-" {
			note = f[5]
		}
		images := ""
		if len(f) >= 7 && f[6] != "-" {
			images = f[6]
		}
		keepForever := len(f) >= 8 && (f[7] == "1" || f[7] == "true" || f[7] == "on")
		snaps = append(snaps, SnapInfo{
			Name:         f[0],
			Kind:         kind,
			Note:         note,
			Images:       images,
			Created:      time.Unix(sec, 0).UTC(),
			UsedBytes:    used,
			DeferDestroy: deferred,
			KeepForever:  keepForever,
			Clones:       clones,
		})
	}
	return snaps, nil
}

// NewerSnapshots returns all fjord-managed snapshots of name created after
// snap, including deferred ones, so callers can preview removals or refuse a
// destructive rollback when a kept copy depends on a newer snapshot.
func NewerSnapshots(ctx context.Context, name, snap string) ([]SnapInfo, error) {
	all, err := ListSnapshots(ctx, name, true)
	if err != nil {
		return nil, err
	}
	for i, s := range all {
		if s.Name == snap {
			return all[i+1:], nil
		}
	}
	return nil, fmt.Errorf("%s not found on %s", snap, name)
}

// Snapshot takes <name>@fjord-update-<unix>. When keep >= 0 it keeps the
// newest keep usable automatic update snapshots; when keep < 0 it skips
// count-based pruning so callers can enforce age-based retention via
// PruneAutomaticSnapshots. Optional imagesJSON records the running service
// images (service -> ref@digest) in fjord:images so restoring this snapshot
// pins the exact version that wrote its data.
func Snapshot(ctx context.Context, name string, at time.Time, keep int, imagesJSON ...string) (string, error) {
	snap := fmt.Sprintf("%s@%s%d", name, UpdateSnapPrefix, at.Unix())
	args := []string{"snapshot"}
	if len(imagesJSON) > 0 && imagesJSON[0] != "" {
		args = append(args, "-o", PropImages+"="+imagesJSON[0])
	}
	args = append(args, snap)
	if _, err := run(ctx, args...); err != nil {
		return "", fmt.Errorf("zfs snapshot %s: %w", snap, err)
	}
	if keep < 0 {
		return snap, nil
	}
	all, err := ListSnapshots(ctx, name, true)
	if err != nil {
		return snap, nil // taken; pruning waits for the next one
	}
	var countable []SnapInfo
	for _, s := range all {
		if s.Kind != "update" || s.DeferDestroy || len(s.Clones) > 0 || s.KeepForever {
			continue
		}
		countable = append(countable, s)
	}
	for len(countable) > keep {
		target := countable[0]
		countable = countable[1:]
		_, _ = run(ctx, "destroy", "-d", target.Name)
	}
	return snap, nil
}

// SnapshotScheduled takes a scheduled daily restore point
// (<name>@fjord-scheduled-<unix>) with optional running image metadata.
func SnapshotScheduled(ctx context.Context, name string, at time.Time, imagesJSON ...string) (string, error) {
	snap := fmt.Sprintf("%s@%s%d", name, ScheduledSnapPrefix, at.Unix())
	args := []string{"snapshot"}
	if len(imagesJSON) > 0 && imagesJSON[0] != "" {
		args = append(args, "-o", PropImages+"="+imagesJSON[0])
	}
	args = append(args, snap)
	if _, err := run(ctx, args...); err != nil {
		return "", fmt.Errorf("zfs snapshot %s: %w", snap, err)
	}
	return snap, nil
}

// PruneAutomaticSnapshots removes automatic restore points (pre-update and
// scheduled) older than maxAge using deferred destroy (-d), while always
// keeping the newest unkept countable automatic restore point whatever its age
// so an app that updates infrequently never loses its last safety net. Manual
// restore points, snapshots marked KeepForever (fjord:keep), and clone-held or
// already-deferred snapshots are never touched.
func PruneAutomaticSnapshots(ctx context.Context, name string, maxAge time.Duration, now time.Time) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	all, err := ListSnapshots(ctx, name, true)
	if err != nil {
		return 0, err
	}
	var countable []SnapInfo
	for _, s := range all {
		if (s.Kind != "update" && s.Kind != "scheduled") || s.DeferDestroy || len(s.Clones) > 0 || s.KeepForever {
			continue
		}
		countable = append(countable, s)
	}
	if len(countable) <= 1 {
		return 0, nil
	}
	pruned := 0
	// Always keep countable[len(countable)-1] (the newest automatic restore point).
	for _, s := range countable[:len(countable)-1] {
		if !s.Created.IsZero() && now.Sub(s.Created) > maxAge {
			if _, err := run(ctx, "destroy", "-d", s.Name); err == nil {
				pruned++
			}
		}
	}
	return pruned, nil
}

// WrittenSince returns ZFS's `written@<snap>` property for name (bytes written
// since fullSnap, ignoring any snapshots from other tools taken in between).
func WrittenSince(ctx context.Context, name, fullSnap string) (int64, error) {
	_, shortName, ok := strings.Cut(fullSnap, "@")
	if !ok || shortName == "" {
		return 0, fmt.Errorf("invalid snapshot name %q", fullSnap)
	}
	out, err := run(ctx, "get", "-Hp", "-o", "value", "written@"+shortName, name)
	if err != nil {
		return 0, fmt.Errorf("zfs get written@%s %s: %w", shortName, name, err)
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("zfs get written@%s %s: unexpected value %q", shortName, name, strings.TrimSpace(string(out)))
	}
	return v, nil
}

// SnapshotExists reports whether fullSnap (<dataset>@<snap>) exists.
func SnapshotExists(ctx context.Context, fullSnap string) bool {
	if !strings.Contains(fullSnap, "@") {
		return false
	}
	_, err := run(ctx, "list", "-H", "-t", "snapshot", "-o", "name", fullSnap)
	return err == nil
}

// Rollback puts name's data back to snap, one of fjord's own snapshots of it.
// Refuses when any newer snapshot has a dependent clone (kept copy) so fjord
// never uses -R or silently destroys a kept copy.
func Rollback(ctx context.Context, name, snap string) error {
	if !IsFjordSnap(name, snap) {
		return fmt.Errorf("%s is not one of fjord's snapshots of %s", snap, name)
	}
	newer, err := NewerSnapshots(ctx, name, snap)
	if err != nil {
		return err
	}
	for _, s := range newer {
		if len(s.Clones) > 0 {
			return fmt.Errorf("cannot roll back past %s: kept copy %s depends on it", s.Name, s.Clones[0])
		}
	}
	if _, err := run(ctx, "rollback", "-r", snap); err != nil {
		return fmt.Errorf("zfs rollback %s: %w", snap, err)
	}
	return nil
}
