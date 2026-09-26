package appjail

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// AppJail keeps no "create command" the way podman does, but everything a
// jail was made with is queryable: the OCI image behind it (buildah's
// container), fstab, exposes, virtual network and address, OCI env/user,
// and the jail parameters (appjail-config). Adoption reads those back into
// a director bundle -- the same shape catalog installs use -- so the jail
// comes up again under fjord with its name, address, mounts and ports.
// Only OCI container jails qualify: a thick jail built from a release has
// no image to rebuild it from.

// jailInfo is what appjail records about one jail.
type jailInfo struct {
	Name, Image, State, Project string
	Network, Address            string
	NAT                         bool
	Exposes                     []exposeEntry
	Mounts                      []mountEntry
	Env                         map[string]string
	User, Workdir               string
	Entrypoint, Args            string
	Params                      []string // appjail-config getAll lines
	HasLimits, HasDevfs         bool
}

type exposeEntry struct{ Host, Jail, Proto string }
type mountEntry struct{ Device, Mountpoint, Type, Options string }

// StackJails lists the jail names a director-backed stack owns, so the
// adopt handler can leave fjord's own jails out of the candidates even when
// director's project state is missing.
func (b *Backend) StackJails(s *stack.Stack) []string {
	var out []string
	for _, sj := range b.serviceJails(s) {
		out = append(out, sj.jail)
	}
	return out
}

// UnmanagedContainers lists every jail with the spec it would become.
// Non-container jails are listed with the reason they can't be adopted.
func (b *Backend) UnmanagedContainers(ctx context.Context) ([]engine.Unmanaged, error) {
	out, err := exec.CommandContext(ctx, "appjail", "jail", "list", "-HIpt", "name", "status", "is_container").Output()
	if err != nil {
		return nil, fmt.Errorf("appjail jail list: %w", err)
	}
	images := map[string]string{}
	for _, c := range buildahContainers(ctx) {
		images[strings.TrimPrefix(c.ContainerName, "appjail-")] = c.ImageName
	}
	projects := directorProjects()
	var list []engine.Unmanaged
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		u := engine.Unmanaged{ID: f[0], Name: f[0], Image: images[f[0]], State: "stopped", Project: projects[f[0]]}
		if f[1] == "UP" {
			u.State = "running"
		}
		if f[2] != "1" {
			u.Unadoptable = "not an OCI container jail: there is no image to rebuild it from"
			list = append(list, u)
			continue
		}
		if u.Image == "" {
			u.Image = recordedImage(ctx, u.Name) // buildah container gone; appjail still knows
		}
		if u.Image == "" {
			u.Unadoptable = "the image this jail was made from is not recorded anywhere"
			list = append(list, u)
			continue
		}
		info := inspectJail(ctx, u.Name)
		info.Image, info.State, info.Project = u.Image, u.State, u.Project
		u.Spec = convertJail(info)
		list = append(list, u)
	}
	return list, nil
}

// RemoveContainer stops and destroys the jail (and its buildah container)
// so the adopted stack can recreate it under the same name.
func (b *Backend) RemoveContainer(ctx context.Context, name string) error {
	exec.CommandContext(ctx, "appjail", "stop", "--", name).Run() // already down is fine
	if out, err := exec.CommandContext(ctx, "appjail", "jail", "destroy", "-Rf", name).CombinedOutput(); err != nil {
		return fmt.Errorf("appjail jail destroy %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// directorProjects maps jail name -> director project, from director's own
// state under the HOME fjord runs it with (<home>/.director/projects/
// <project>/<service>/name).
func directorProjects() map[string]string {
	home := ""
	if u, err := user.Current(); err == nil {
		home = u.HomeDir
	}
	out := map[string]string{}
	if home == "" {
		return out
	}
	names, _ := filepath.Glob(filepath.Join(home, ".director", "projects", "*", "*", "name"))
	for _, n := range names {
		if b, err := os.ReadFile(n); err == nil {
			out[strings.TrimSpace(string(b))] = filepath.Base(filepath.Dir(filepath.Dir(n)))
		}
	}
	return out
}

// recordedImage reads container_image from the jail's own config.conf under
// JAILDIR, resolved the way appjail resolves it (appjail.conf may move it).
func recordedImage(ctx context.Context, name string) string {
	b, err := os.ReadFile(filepath.Join(jailsDir(ctx), name, "conf", "config.conf"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "container_image:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// inspectJail gathers a jail's settings with the appjail CLI. Every query is
// best-effort: a missing piece becomes a note, never a failure.
func inspectJail(ctx context.Context, name string) jailInfo {
	run := func(args ...string) string {
		out, _ := exec.CommandContext(ctx, args[0], args[1:]...).Output()
		return strings.TrimSpace(string(out))
	}
	// last line only: `jail get` prints a header with these flags, `list` doesn't
	lastLine := func(s string) string {
		lines := strings.Split(strings.TrimSpace(s), "\n")
		return lines[len(lines)-1]
	}
	info := jailInfo{Name: name, Env: map[string]string{}}
	if f := strings.Fields(lastLine(run("appjail", "jail", "get", "-HIpt", "--", name, "networks", "network_ip4"))); len(f) >= 2 && f[0] != "-" {
		info.Network = strings.Split(f[0], ",")[0]
		if f[1] != "-" {
			info.Address = strings.Split(f[1], ",")[0]
		}
	}
	for _, line := range strings.Split(run("appjail", "expose", "list", "-HIpt", name, "hport", "jport", "protocol"), "\n") {
		if f := strings.Fields(line); len(f) == 3 {
			info.Exposes = append(info.Exposes, exposeEntry{f[0], f[1], f[2]})
		}
	}
	for _, line := range strings.Split(run("appjail", "fstab", "jail", name, "list", "-HIpt", "device", "mountpoint", "type", "options"), "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			m := mountEntry{Device: f[0], Mountpoint: f[1]}
			if len(f) > 2 {
				m.Type = f[2]
			}
			if len(f) > 3 {
				m.Options = f[3]
			}
			info.Mounts = append(info.Mounts, m)
		}
	}
	for _, k := range strings.Fields(run("appjail", "oci", "ls-env", name)) {
		info.Env[k] = run("appjail", "oci", "get-env", name, k)
	}
	info.User = run("appjail", "oci", "get-user", name)
	info.Entrypoint = run("appjail", "oci", "get-entrypoint", name)
	info.Args = run("appjail", "oci", "get-args", name)
	info.Workdir = run("appjail", "oci", "get-workdir", name)
	for _, line := range strings.Split(run("appjail-config", "getAll", "-j", name), "\n") {
		if strings.TrimSpace(line) != "" {
			info.Params = append(info.Params, line)
		}
	}
	info.NAT = strings.Contains(strings.Join(info.Params, "\n"), "appjail nat on")
	info.HasLimits = run("appjail", "limits", "list", "-HIpt", name) != ""
	info.HasDevfs = run("appjail", "devfs", "list", "-HIpt", name) != ""
	return info
}

// plumbingRe matches the jail parameters appjail itself adds for the
// virtual network, NAT and exposes; director re-adds them from the spec,
// so they must not be baked into template.conf.
var plumbingRe = regexp.MustCompile(`^(vnet($|\.interface:)|exec\.(prestart|poststart|poststop)\+?:.*\bappjail (network|nat|expose) )`)

// convertJail turns what appjail recorded into the stack files.
func convertJail(info jailInfo) *engine.AdoptSpec {
	svc := composepkg.ServiceName(info.Image)
	spec := &engine.AdoptSpec{Service: svc}
	envFile := map[string]string{}
	w := func(sb *strings.Builder, format string, a ...any) { fmt.Fprintf(sb, format, a...) }

	// Values a person would tune move to .env; the rest stay literal.
	envRef := func(k, v string) string {
		if k == "PUID" || k == "PGID" || k == "TZ" {
			envFile[k] = v
			return "${" + k + "}"
		}
		return v
	}
	envKeys := sortedKeys(info.Env)
	envValues := map[string]string{}
	for _, k := range envKeys {
		envValues[k] = envRef(k, info.Env[k])
	}

	// Mounts: device path in .env under a name derived from the mountpoint,
	// the way catalog bundles do (RADARR_CONFIG_PATH).
	type vol struct{ Var, Mount string }
	var vols []vol
	for _, m := range info.Mounts {
		if m.Type != "" && m.Type != "<pseudofs>" && m.Type != "nullfs" {
			spec.Notes = append(spec.Notes, fmt.Sprintf("mount %s (%s) is not a nullfs bind and was left out", m.Mountpoint, m.Type))
			continue
		}
		v := envVarName(svc, m.Mountpoint) + "_PATH"
		envFile[v] = m.Device
		vols = append(vols, vol{v, m.Mountpoint})
	}

	// --- director spec
	var d strings.Builder
	if info.Network != "" {
		w(&d, "options:\n  - virtualnet: '%s:<random> default", info.Network)
		if info.Address != "" {
			w(&d, " address:%s", info.Address)
		}
		w(&d, "'\n")
		if info.NAT {
			w(&d, "  - nat:\n")
		}
	} else {
		spec.Notes = append(spec.Notes, "no virtual network on this jail: check its network parameters in template.conf")
	}
	w(&d, "services:\n  %s:\n    name: %s\n    options:\n", svc, info.Name)
	for _, e := range info.Exposes {
		w(&d, "      - expose: '%s:%s proto:%s'\n", e.Host, e.Jail, e.Proto)
	}
	w(&d, "      - template: !ENV '${PWD}/template.conf'\n")
	if info.User != "" || info.Workdir != "" || info.Entrypoint != "" || info.Args != "" || len(envKeys) > 0 {
		w(&d, "    oci:\n")
	}
	if info.User != "" {
		w(&d, "      user: %s\n", info.User)
	}
	if info.Workdir != "" {
		w(&d, "      workdir: %s\n", info.Workdir)
	}
	if info.Entrypoint != "" {
		w(&d, "      entrypoint: [%s]\n", yamlList(ociWords(info.Entrypoint)))
	}
	if info.Args != "" {
		w(&d, "      arguments: [%s]\n", yamlList(ociWords(info.Args)))
	}
	if len(envKeys) > 0 {
		w(&d, "      environment:\n")
		for _, k := range envKeys {
			if strings.HasPrefix(envValues[k], "${") {
				w(&d, "        - %s: !ENV '%s'\n", k, envValues[k])
			} else {
				w(&d, "        - %s: %s\n", k, yamlScalar(envValues[k]))
			}
		}
	}
	if len(vols) > 0 {
		w(&d, "    volumes:\n")
		for _, v := range vols {
			w(&d, "      - %s: %s\n", v.Var, v.Mount)
		}
		w(&d, "volumes:\n")
		for _, v := range vols {
			w(&d, "  %s:\n    device: !ENV '${%s}'\n", v.Var, v.Var)
		}
	}
	spec.Director = d.String()

	// --- Makejail: the image, tag kept as an ARG like catalog bundles
	repo, tag := info.Image, "latest"
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo, tag = repo[:i], repo[i+1:]
	}
	spec.Makejail = fmt.Sprintf("# Makejail\n\nARG tag=%s\n\nOPTION container=boot\nOPTION overwrite=force\nOPTION from=%s:${tag}\n", tag, repo)

	// --- template.conf: the jail's parameters minus appjail's own plumbing
	var t strings.Builder
	w(&t, "# template.conf\n\n")
	for _, p := range info.Params {
		if !plumbingRe.MatchString(p) {
			w(&t, "%s\n", p)
		}
	}
	spec.Template = t.String()

	// --- compose: what the UI, status and the Open link read
	var c strings.Builder
	w(&c, "services:\n  %s:\n    image: %s\n    container_name: %s\n", svc, info.Image, info.Name)
	if len(envKeys) > 0 {
		w(&c, "    environment:\n")
		for _, k := range envKeys {
			w(&c, "      - %s=%s\n", k, envValues[k])
		}
	}
	if len(vols) > 0 {
		w(&c, "    volumes:\n")
		for _, v := range vols {
			w(&c, "      - \"${%s}:%s\"\n", v.Var, v.Mount)
		}
	}
	if len(info.Exposes) > 0 {
		w(&c, "    ports:\n")
		for _, e := range info.Exposes {
			if e.Proto == "udp" {
				w(&c, "      - \"%s:%s/udp\"\n", e.Host, e.Jail)
			} else {
				w(&c, "      - \"%s:%s\"\n", e.Host, e.Jail)
			}
		}
	}
	spec.Compose = c.String()

	var e strings.Builder
	for _, k := range sortedKeys(envFile) {
		w(&e, "%s=%s\n", k, envFile[k])
	}
	spec.Env = e.String()

	if info.HasLimits {
		spec.Notes = append(spec.Notes, "resource limits (appjail limits) are not carried over")
	}
	if info.HasDevfs {
		spec.Notes = append(spec.Notes, "custom devfs rules are not carried over")
	}
	if info.Project != "" {
		spec.Notes = append(spec.Notes, fmt.Sprintf("made by appjail-director project %q outside fjord; that project's state stays in ~/.director", info.Project))
	}
	return spec
}

// envVarName builds an upper-case identifier from a service and a mount
// path: ("uptime-kuma", "/config") -> "UPTIME_KUMA_CONFIG".
func envVarName(svc, mount string) string {
	clean := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToUpper(s) {
			if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			} else {
				b.WriteByte('_')
			}
		}
		return strings.Trim(b.String(), "_")
	}
	return clean(svc) + "_" + clean(mount)
}

// yamlScalar quotes a value unless it is plainly safe as a YAML scalar.
func yamlScalar(v string) string {
	if v != "" && !strings.ContainsAny(v, ":#{}[],&*?|<>=!%@`'\" ") && !strings.HasPrefix(v, "-") {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// ociWords splits what `appjail oci get-args` / get-entrypoint answer: each
// word double-quoted, `\"` and `\\` escaped ("redis-server" "very verbose").
// Splitting that on spaces kept the quotes as part of every word -- the jail
// then looked for a program named "redis-server", quotes included -- and cut
// "very verbose" in two. Unquoted text is split on spaces.
func ociWords(s string) []string {
	var words []string
	for i := 0; i < len(s); {
		switch {
		case s[i] == ' ' || s[i] == '\t' || s[i] == '\n':
			i++
		case s[i] == '"':
			var b strings.Builder
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
				i++
			}
			i++ // closing quote
			words = append(words, b.String())
		default:
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != '\t' && s[j] != '\n' {
				j++
			}
			words = append(words, s[i:j])
			i = j
		}
	}
	return words
}

// yamlList renders words as a YAML flow list of single-quoted strings.
func yamlList(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = "'" + strings.ReplaceAll(w, "'", "''") + "'"
	}
	return strings.Join(q, ", ")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jailsDir is where appjail keeps its jails, resolved the way appjail resolves
// it -- appjail.conf may move it.
// jailsDirOverride replaces the resolved path in tests.
var jailsDirOverride string

func jailsDir(ctx context.Context) string {
	if jailsDirOverride != "" {
		return jailsDirOverride
	}
	dir := "/usr/local/appjail/jails"
	if out, err := exec.CommandContext(ctx, "sh", "-c",
		`. /usr/local/share/appjail/files/config.conf && printf %s "$JAILDIR"`).Output(); err == nil &&
		strings.TrimSpace(string(out)) != "" {
		dir = strings.TrimSpace(string(out))
	}
	return dir
}

// missingDHClient reports whether a running jail's image lacks the dhclient
// script, which is what a DHCP interface needs.
//
// appjail writes ifconfig_<iface>="SYNCDHCP" and the JAIL runs it, so the
// image has to carry it. Without it rc fails with
// "eval: /etc/rc.d/dhclient: not found" and the whole netif stage aborts --
// taking any static interface on the same jail down with it -- and the jail
// comes up holding no address at all. Reporting that the DHCP server did not
// answer sends someone to the wrong machine: nothing ever asked it.
func missingDHClient(ctx context.Context, jail string) bool {
	root := filepath.Join(jailsDir(ctx), jail, "jail")
	// Only answer for a jail whose filesystem is actually there. A jail that
	// is not mounted would look like every image lacks dhclient.
	if _, err := os.Stat(filepath.Join(root, "etc", "rc.d")); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(root, "etc", "rc.d", "dhclient"))
	return os.IsNotExist(err)
}
