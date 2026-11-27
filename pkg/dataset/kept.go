package dataset

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// KeptCopy describes a read-only kept dataset set aside during a restore or
// migration.
type KeptCopy struct {
	Dataset    string    `json:"dataset"`
	Stack      string    `json:"stack,omitempty"`
	Mountpoint string    `json:"mountpoint"`
	Kind       string    `json:"kind"`
	CreatedAt  time.Time `json:"createdAt"`
	Until      string    `json:"until,omitempty"` // RFC3339, or "" when kept forever
	Version    string    `json:"version,omitempty"`
	Image      string    `json:"image,omitempty"` // single ref@digest or JSON map service->ref@digest
	UsedBytes  int64     `json:"usedBytes"`
}

// LocalProps holds user-tuned local or received ZFS properties captured from
// a live dataset so they can be re-applied onto a clone.
type LocalProps struct {
	Props   map[string]string
	LocalMP string // non-empty when mountpoint was locally set (not inherited)
}

// Exists reports whether a ZFS filesystem dataset currently exists.
func Exists(ctx context.Context, name string) bool {
	if name == "" {
		return false
	}
	_, err := run(ctx, "list", "-H", "-o", "name", "-t", "filesystem", name)
	return err == nil
}

// Mountpoint returns the current mountpoint of dataset name, or "" if none.
func Mountpoint(ctx context.Context, name string) string {
	if name == "" {
		return ""
	}
	out, err := run(ctx, "list", "-H", "-o", "mountpoint", "-t", "filesystem", name)
	if err != nil {
		return ""
	}
	mp := strings.TrimSpace(string(out))
	if mp == "" || mp == "none" || mp == "legacy" || mp == "-" {
		return ""
	}
	return mp
}

// HasZFS reports whether zfs is available with at least one filesystem.
func HasZFS(ctx context.Context) bool {
	out, err := run(ctx, "list", "-H", "-o", "name", "-t", "filesystem")
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// requireKept verifies that name belongs to stack AND carries fjord:kept-kind
// so kept-copy operations can never touch or destroy a stack's live dataset.
func requireKept(ctx context.Context, name, stack string) error {
	if err := Owned(ctx, name, stack); err != nil {
		return err
	}
	out, err := run(ctx, "get", "-H", "-o", "value", PropKeptKind, name)
	if err != nil {
		return fmt.Errorf("zfs get %s %s: %w", PropKeptKind, name, err)
	}
	kind := strings.TrimSpace(string(out))
	if kind == "" || kind == "-" {
		return fmt.Errorf("%s is not a kept copy (refusing to modify live dataset)", name)
	}
	return nil
}

// LocalProperties captures locally set and received properties on name,
// excluding fjord's kept-copy tags, readonly, and read-only ZFS stats.
func LocalProperties(ctx context.Context, name string) (LocalProps, error) {
	out, err := run(ctx, "get", "-H", "-o", "property,value,source", "-s", "local,received", "all", name)
	if err != nil {
		return LocalProps{}, fmt.Errorf("zfs get local %s: %w", name, err)
	}
	lp := LocalProps{Props: map[string]string{}}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		prop, val := f[0], f[1]
		if prop == Tag || prop == "readonly" || strings.HasPrefix(prop, "fjord:kept-") {
			continue
		}
		if prop == "mountpoint" {
			if val != "none" && val != "legacy" && strings.HasPrefix(val, "/") {
				lp.LocalMP = val
			}
			continue
		}
		if !isCloneSettable(prop) {
			continue
		}
		lp.Props[prop] = val
	}
	return lp, nil
}

// isCloneSettable filters out ZFS properties that are read-only or only valid
// at dataset creation, keeping user-tuned properties and custom user props.
func isCloneSettable(prop string) bool {
	if strings.Contains(prop, ":") {
		return true // custom user property
	}
	switch prop {
	case "aclinherit", "aclmode", "acltype", "atime", "canmount", "checksum",
		"compression", "copies", "dedup", "devices", "dnodesize", "exec",
		"logbias", "nbmand", "overlay", "primarycache", "quota", "recordsize",
		"redundant_metadata", "refquota", "refreservation", "relatime",
		"reservation", "secondarycache", "setuid", "sharenfs", "sharesmb",
		"snapdir", "special_small_blocks", "sync", "vscan", "xattr", "zoned":
		return true
	}
	return false
}

// RestoreWithKeep sets liveName aside as a read-only kept dataset, clones
// snap back into liveName (preserving local/received properties), and promotes
// liveName so the kept copy can be destroyed independently after ttl.
// Kept properties are written immediately after the first rename so a crash
// after promote never leaves an untagged, invisible kept dataset.
func RestoreWithKeep(ctx context.Context, liveName, snap, stack, version, image string, now time.Time, ttl time.Duration) (string, error) {
	if err := Owned(ctx, liveName, stack); err != nil {
		return "", err
	}
	if !IsFjordSnap(liveName, snap) {
		return "", fmt.Errorf("%s is not one of fjord's snapshots of %s", snap, liveName)
	}
	_, snapPart, ok := strings.Cut(snap, "@")
	if !ok || snapPart == "" {
		return "", fmt.Errorf("invalid snapshot name: %q", snap)
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
	// Tag kept properties right after rename so a crash after promote cannot
	// leave an untagged copy that LiveByTag mistakes for the live dataset.
	setKeptProps(ctx, keptName, stack, "rollback", version, image, now, ttl)

	cloneArgs := []string{"clone", "-o", Tag + "=" + stack}
	for k, v := range lp.Props {
		cloneArgs = append(cloneArgs, "-o", k+"="+v)
	}
	if lp.LocalMP != "" {
		cloneArgs = append(cloneArgs, "-o", "mountpoint="+lp.LocalMP)
	}
	cloneArgs = append(cloneArgs, keptName+"@"+snapPart, liveName)

	if _, err := run(ctx, cloneArgs...); err != nil {
		clearKeptProps(ctx, keptName)
		_, _ = run(ctx, "set", "readonly=off", keptName)
		if lp.LocalMP != "" {
			_, _ = run(ctx, "set", "mountpoint="+lp.LocalMP, keptName)
		}
		_, _ = run(ctx, "rename", "-f", keptName, liveName)
		return "", fmt.Errorf("zfs clone %s: %w", liveName, err)
	}

	if _, err := run(ctx, "promote", liveName); err != nil {
		_, _ = run(ctx, "destroy", liveName)
		clearKeptProps(ctx, keptName)
		_, _ = run(ctx, "set", "readonly=off", keptName)
		if lp.LocalMP != "" {
			_, _ = run(ctx, "set", "mountpoint="+lp.LocalMP, keptName)
		}
		_, _ = run(ctx, "rename", "-f", keptName, liveName)
		return "", fmt.Errorf("zfs promote %s: %w", liveName, err)
	}

	return keptName, nil
}

func setKeptProps(ctx context.Context, keptName, stack, kind, version, image string, now time.Time, ttl time.Duration) {
	args := []string{
		"set",
		"readonly=on",
		Tag + "=" + stack,
		PropKeptKind + "=" + kind,
		PropKeptAt + "=" + now.UTC().Format(time.RFC3339),
	}
	if ttl > 0 {
		args = append(args, PropKeptUntil+"="+now.UTC().Add(ttl).Format(time.RFC3339))
	}
	if version != "" {
		args = append(args, PropKeptVersion+"="+version)
	}
	if image != "" {
		args = append(args, PropKeptImage+"="+image)
	}
	args = append(args, keptName)
	_, _ = run(ctx, args...)
}

func clearKeptProps(ctx context.Context, name string) {
	for _, prop := range []string{PropKeptKind, PropKeptAt, PropKeptUntil, PropKeptVersion, PropKeptImage} {
		_, _ = run(ctx, "inherit", prop, name)
	}
}

// RestoreKept brings keptName back as liveName. When keepCurrent is true,
// the previous liveName is renamed aside as a new kept copy (tagged
// immediately after rename). When false, it uses a safe promote ->
// rename(.deleting) -> rename(live) -> destroy sequence.
func RestoreKept(ctx context.Context, liveName, keptName, stack, prevVersion, prevImage string, keepCurrent bool, now time.Time, ttl time.Duration) (string, error) {
	if err := Owned(ctx, liveName, stack); err != nil {
		return "", err
	}
	if err := requireKept(ctx, keptName, stack); err != nil {
		return "", err
	}
	lp, _ := LocalProperties(ctx, liveName)

	if !keepCurrent {
		if snaps, err := ListSnapshots(ctx, liveName, true); err == nil {
			for _, s := range snaps {
				for _, c := range s.Clones {
					if c != keptName {
						return "", fmt.Errorf("cannot discard current data: kept copy %s depends on %s", c, s.Name)
					}
				}
			}
		}
	}

	if _, err := run(ctx, "set", "readonly=off", keptName); err != nil {
		return "", fmt.Errorf("zfs set readonly=off %s: %w", keptName, err)
	}

	if keepCurrent {
		suffix := ".kept-" + now.UTC().Format("2006-01-02-150405")
		newKept := liveName + suffix
		if _, err := run(ctx, "rename", liveName, newKept); err != nil {
			_, _ = run(ctx, "set", "readonly=on", keptName)
			return "", fmt.Errorf("zfs rename %s -> %s: %w", liveName, newKept, err)
		}
		if lp.LocalMP != "" {
			_, _ = run(ctx, "set", "mountpoint="+lp.LocalMP+suffix, newKept)
		}
		setKeptProps(ctx, newKept, stack, "rollback", prevVersion, prevImage, now, ttl)

		if _, err := run(ctx, "rename", "-f", keptName, liveName); err != nil {
			clearKeptProps(ctx, newKept)
			_, _ = run(ctx, "set", "readonly=off", newKept)
			if lp.LocalMP != "" {
				_, _ = run(ctx, "set", "mountpoint="+lp.LocalMP, newKept)
			}
			_, _ = run(ctx, "rename", "-f", newKept, liveName)
			_, _ = run(ctx, "set", "readonly=on", keptName)
			return "", fmt.Errorf("zfs rename %s -> %s: %w", keptName, liveName, err)
		}
		if lp.LocalMP != "" {
			_, _ = run(ctx, "set", "mountpoint="+lp.LocalMP, liveName)
		}
		_, _ = run(ctx, "promote", liveName)
		clearKeptProps(ctx, liveName)
		return newKept, nil
	}

	dropName := fmt.Sprintf("%s.deleting-%d", liveName, now.Unix())
	_, _ = run(ctx, "promote", keptName)
	if _, err := run(ctx, "rename", liveName, dropName); err != nil {
		_, _ = run(ctx, "promote", liveName)
		_, _ = run(ctx, "set", "readonly=on", keptName)
		return "", fmt.Errorf("zfs rename %s -> %s: %w", liveName, dropName, err)
	}
	if _, err := run(ctx, "rename", "-f", keptName, liveName); err != nil {
		_, _ = run(ctx, "rename", "-f", dropName, liveName)
		_, _ = run(ctx, "promote", liveName)
		_, _ = run(ctx, "set", "readonly=on", keptName)
		return "", fmt.Errorf("zfs rename %s -> %s: %w", keptName, liveName, err)
	}
	if lp.LocalMP != "" {
		_, _ = run(ctx, "set", "mountpoint="+lp.LocalMP, liveName)
	}
	clearKeptProps(ctx, liveName)
	_, _ = run(ctx, "destroy", "-r", dropName)
	return "", nil
}

// ListKept returns all kept-copy datasets belonging to stack. When stack is
// "", returns kept-copy datasets for all stacks (used by the expiry sweep).
func ListKept(ctx context.Context, stack string) ([]KeptCopy, error) {
	props := strings.Join([]string{
		"name", "mountpoint", "used", Tag,
		PropKeptKind, PropKeptAt, PropKeptUntil, PropKeptVersion, PropKeptImage,
	}, ",")
	out, err := run(ctx, "list", "-H", "-p", "-o", props, "-t", "filesystem")
	if err != nil {
		return nil, err
	}
	var res []KeptCopy
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 9 {
			continue
		}
		if f[4] == "" || f[4] == "-" {
			continue // not a kept copy
		}
		if stack != "" && f[3] != stack {
			continue
		}
		used, _ := strconv.ParseInt(f[2], 10, 64)
		at, _ := time.Parse(time.RFC3339, f[5])
		until := f[6]
		if until == "-" {
			until = ""
		}
		ver := f[7]
		if ver == "-" {
			ver = ""
		}
		img := f[8]
		if img == "-" {
			img = ""
		}
		res = append(res, KeptCopy{
			Dataset:    f[0],
			Stack:      f[3],
			Mountpoint: f[1],
			Kind:       f[4],
			CreatedAt:  at,
			Until:      until,
			Version:    ver,
			Image:      img,
			UsedBytes:  used,
		})
	}
	return res, nil
}

// SetKeptForever toggles whether keptName auto-expires. Refuses if keptName
// is not a kept copy of stack.
func SetKeptForever(ctx context.Context, keptName, stack string, forever bool, until time.Time) error {
	if err := requireKept(ctx, keptName, stack); err != nil {
		return err
	}
	if forever {
		if _, err := run(ctx, "inherit", PropKeptUntil, keptName); err != nil {
			return fmt.Errorf("zfs inherit %s %s: %w", PropKeptUntil, keptName, err)
		}
		return nil
	}
	if _, err := run(ctx, "set", PropKeptUntil+"="+until.UTC().Format(time.RFC3339), keptName); err != nil {
		return fmt.Errorf("zfs set %s %s: %w", PropKeptUntil, keptName, err)
	}
	return nil
}

// promoteDependentClones promotes any clones that depend on snapshots owned
// by ds (which happens after a second restore to an older point), leaving ds
// with no dependent clones so zfs destroy -r succeeds cleanly.
func promoteDependentClones(ctx context.Context, ds string) {
	out, err := run(ctx, "list", "-H", "-t", "snapshot", "-o", "clones", "-d", "1", ds)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || line == "-" {
			continue
		}
		for _, clone := range strings.Split(line, ",") {
			clone = strings.TrimSpace(clone)
			if clone == "" || clone == "-" || seen[clone] {
				continue
			}
			seen[clone] = true
			_, _ = run(ctx, "promote", clone)
		}
	}
}

// DestroyKept removes a kept-copy dataset belonging to stack and returns its
// approximate used bytes prior to destruction. Refuses if keptName is not a
// kept copy (protecting live datasets), and promotes any dependent clones
// first so chained restores can always be deleted.
func DestroyKept(ctx context.Context, keptName, stack string) (int64, error) {
	if err := requireKept(ctx, keptName, stack); err != nil {
		return 0, err
	}
	var used int64
	if out, err := run(ctx, "list", "-H", "-p", "-o", "used", keptName); err == nil {
		used, _ = strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	}
	promoteDependentClones(ctx, keptName)
	if _, err := run(ctx, "destroy", "-f", "-r", keptName); err != nil {
		return 0, fmt.Errorf("zfs destroy %s: %w", keptName, err)
	}
	return used, nil
}

// SnapshotManual creates a user-initiated restore point on name
// (<name>@fjord-manual-<unix>) with optional fjord:note and fjord:images.
// Manual snapshots are never pruned by Snapshot().
func SnapshotManual(ctx context.Context, name, stack, note string, at time.Time, imagesJSON ...string) (string, error) {
	if err := Owned(ctx, name, stack); err != nil {
		return "", err
	}
	snap := fmt.Sprintf("%s@%s%d", name, ManualSnapPrefix, at.Unix())
	note = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(note, "\r", " "), "\n", " "))
	if len(note) > 120 {
		note = note[:120]
	}
	args := []string{"snapshot"}
	if note != "" {
		args = append(args, "-o", PropNote+"="+note)
	}
	if len(imagesJSON) > 0 && imagesJSON[0] != "" {
		args = append(args, "-o", PropImages+"="+imagesJSON[0])
	}
	args = append(args, snap)
	if _, err := run(ctx, args...); err != nil {
		return "", fmt.Errorf("zfs snapshot %s: %w", snap, err)
	}
	return snap, nil
}

// DestroyManualSnapshot removes a manual restore point on name using deferred
// destroy (-d) so an active kept copy still referencing it does not fail.
func DestroyManualSnapshot(ctx context.Context, name, snap, stack string) (int64, error) {
	if err := Owned(ctx, name, stack); err != nil {
		return 0, err
	}
	if !IsManualSnap(name, snap) {
		return 0, fmt.Errorf("%s is not a manual restore point of %s", snap, name)
	}
	var used int64
	if out, err := run(ctx, "list", "-H", "-p", "-o", "used", snap); err == nil {
		used, _ = strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	}
	if _, err := run(ctx, "destroy", "-d", snap); err != nil {
		return 0, fmt.Errorf("zfs destroy %s: %w", snap, err)
	}
	return used, nil
}

// ReconcilePending recovers from a crash mid-rename/clone/promote before
// start-on-boot brings a stack up.
func ReconcilePending(ctx context.Context, liveName, keptName, newKeptName, dropName, localMP string) string {
	liveExists := Exists(ctx, liveName)
	keptExists := Exists(ctx, keptName)
	newKeptExists := Exists(ctx, newKeptName)
	dropExists := Exists(ctx, dropName)

	if !liveExists {
		if dropExists {
			if _, err := run(ctx, "rename", "-f", dropName, liveName); err == nil {
				if localMP != "" {
					_, _ = run(ctx, "set", "mountpoint="+localMP, liveName)
				}
				_, _ = run(ctx, "promote", liveName)
				clearKeptProps(ctx, liveName)
				if keptExists {
					_, _ = run(ctx, "set", "readonly=on", keptName)
				}
				return "restored live dataset from interrupted delete swap"
			}
		}
		if newKeptExists {
			_, _ = run(ctx, "set", "readonly=off", newKeptName)
			if _, err := run(ctx, "rename", "-f", newKeptName, liveName); err == nil {
				if localMP != "" {
					_, _ = run(ctx, "set", "mountpoint="+localMP, liveName)
				}
				clearKeptProps(ctx, liveName)
				if keptExists {
					_, _ = run(ctx, "set", "readonly=on", keptName)
				}
				return "restored live dataset from interrupted kept-copy swap"
			}
		}
		if keptExists {
			_, _ = run(ctx, "set", "readonly=off", keptName)
			if _, err := run(ctx, "rename", "-f", keptName, liveName); err == nil {
				if localMP != "" {
					_, _ = run(ctx, "set", "mountpoint="+localMP, liveName)
				}
				clearKeptProps(ctx, liveName)
				return "restored live dataset from interrupted restore"
			}
		}
		return ""
	}
	clearKeptProps(ctx, liveName)
	if dropExists {
		_, _ = run(ctx, "destroy", "-f", "-r", dropName)
		return "cleaned up interrupted delete dataset"
	}
	if newKeptExists {
		_, _ = run(ctx, "promote", liveName)
		_, _ = run(ctx, "set", "readonly=on", newKeptName)
		return "completed interrupted kept-copy swap"
	}
	if keptExists {
		_, _ = run(ctx, "promote", liveName)
		_, _ = run(ctx, "set", "readonly=on", keptName)
		return "completed interrupted promote and read-only lock"
	}
	return ""
}
