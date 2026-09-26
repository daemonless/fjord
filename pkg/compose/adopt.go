package compose

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Adopted is a compose stack derived from an existing container's original
// `podman run` line (podman keeps it in .Config.CreateCommand), so a
// hand-started container can become a fjord stack with the same image,
// mounts, network address and jail parameters -- and, via container_name,
// the same name.
type Adopted struct {
	Service string   // compose service name (image basename)
	Compose string   // compose.yaml
	Env     string   // .env: PUID/PGID/TZ and the like
	Notes   []string // run flags that were dropped or need a look
}

// FromRunArgs turns `podman run ...` argv (with or without the leading
// "podman run"; `podman create`, `podman container run|create` and a full
// path to podman are the same thing) into a stack. Recognised: --name, --hostname, --network,
// --ip, --mac-address, --annotation, -e/--env, -v/--volume, -p/--publish,
// --restart, --user, --privileged, --cap-add, --device, --dns, --label, plus
// the image and command. Anything else is reported in Notes rather than
// silently lost. The FreeBSD podman doesn't report a container's IP/MAC via
// inspect, which is why the run line is the source of truth.
func FromRunArgs(args []string) (*Adopted, error) {
	// Anything else after "podman" is refused rather than read as flags: the
	// word "podman" would become the image and the rest its command.
	if len(args) > 0 && path.Base(args[0]) == "podman" {
		rest := args[1:]
		if len(rest) > 0 && rest[0] == "container" {
			rest = rest[1:]
		}
		if len(rest) == 0 || (rest[0] != "run" && rest[0] != "create") {
			return nil, fmt.Errorf("not a podman run or create line: %s", strings.Join(args, " "))
		}
		args = rest[1:]
	}
	var (
		name, hostname, network, ip, mac, restart, user string
		privileged                                      bool
		annotations, labels                             = map[string]string{}, map[string]string{}
		envs, volumes, ports, capAdd, devices, dns      []string
		notes                                           []string
		image                                           string
		command                                         []string
	)
	take := func(i *int, flag string) (string, bool) {
		if *i+1 >= len(args) {
			notes = append(notes, flag+" had no value")
			return "", false
		}
		*i++
		return args[*i], true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if image != "" {
			command = append(command, a)
			continue
		}
		flag, val, joined := a, "", false
		if strings.HasPrefix(a, "--") {
			if eq := strings.IndexByte(a, '='); eq > 0 {
				flag, val, joined = a[:eq], a[eq+1:], true
			}
		}
		get := func() (string, bool) {
			if joined {
				return val, true
			}
			return take(&i, flag)
		}
		switch flag {
		case "-d", "--detach", "-t", "--tty", "-i", "--interactive", "--rm", "--init":
			// lifecycle flags: compose handles these
		case "--name":
			name, _ = get()
		case "--hostname", "-h":
			hostname, _ = get()
		case "--network", "--net":
			network, _ = get()
		case "--ip":
			ip, _ = get()
		case "--mac-address":
			mac, _ = get()
		case "--restart":
			restart, _ = get()
		case "--user", "-u":
			user, _ = get()
		case "--privileged":
			privileged = true
		case "--annotation":
			if v, ok := get(); ok {
				k, vv, _ := strings.Cut(v, "=")
				annotations[k] = vv
			}
		case "--label", "-l":
			if v, ok := get(); ok {
				k, vv, _ := strings.Cut(v, "=")
				labels[k] = vv
			}
		case "-e", "--env":
			if v, ok := get(); ok {
				envs = append(envs, v)
			}
		case "-v", "--volume":
			if v, ok := get(); ok {
				volumes = append(volumes, v)
			}
		case "-p", "--publish":
			if v, ok := get(); ok {
				ports = append(ports, v)
			}
		case "--cap-add":
			if v, ok := get(); ok {
				capAdd = append(capAdd, v)
			}
		case "--device":
			if v, ok := get(); ok {
				devices = append(devices, v)
			}
		case "--dns":
			if v, ok := get(); ok {
				dns = append(dns, v)
			}
		default:
			if strings.HasPrefix(a, "-") {
				// Unknown flag: assume it takes a value if the next arg isn't a flag.
				if !joined && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					notes = append(notes, fmt.Sprintf("dropped %s %s", a, args[i]))
				} else {
					notes = append(notes, "dropped "+a)
				}
				continue
			}
			image = a
		}
	}
	if image == "" {
		return nil, fmt.Errorf("no image in run arguments")
	}
	svc := ServiceName(image)

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("services:\n  %s:\n    image: %s\n", svc, image)
	if name != "" {
		w("    container_name: %s\n", name)
	}
	if hostname != "" {
		w("    hostname: %s\n", hostname)
	}
	if restart == "" {
		restart = "unless-stopped"
	}
	w("    restart: %s\n", restart)
	if user != "" {
		w("    user: %q\n", user)
	}
	if privileged {
		w("    privileged: true\n")
	}
	if mac != "" {
		w("    mac_address: %s\n", mac)
	}
	if len(command) > 0 {
		w("    command: [%s]\n", quoteList(command))
	}
	// Environment: PUID/PGID/TZ become .env variables (the values the wizard
	// would have asked for); everything else stays literal.
	envFile := map[string]string{}
	var literal []string
	for _, e := range envs {
		k, v, _ := strings.Cut(e, "=")
		switch k {
		case "PUID", "PGID", "TZ":
			envFile[k] = v
		default:
			literal = append(literal, e)
		}
	}
	if len(envFile) > 0 || len(literal) > 0 {
		w("    environment:\n")
		for _, k := range sortedKeys(envFile) {
			w("      - %s=${%s}\n", k, k)
		}
		for _, e := range literal {
			w("      - %q\n", e)
		}
	}
	if len(volumes) > 0 {
		w("    volumes:\n")
		for _, v := range volumes {
			w("      - %q\n", v)
		}
	}
	if len(ports) > 0 {
		w("    ports:\n")
		for _, p := range ports {
			w("      - %q\n", p)
		}
	}
	if len(devices) > 0 {
		w("    devices:\n")
		for _, d := range devices {
			w("      - %q\n", d)
		}
	}
	if len(capAdd) > 0 {
		w("    cap_add:\n")
		for _, c := range capAdd {
			w("      - %s\n", c)
		}
	}
	if len(dns) > 0 {
		w("    dns:\n")
		for _, d := range dns {
			w("      - %s\n", d)
		}
	}
	if len(annotations) > 0 {
		w("    annotations:\n")
		for _, k := range sortedKeys(annotations) {
			w("      %s: %q\n", k, annotations[k])
		}
	}
	if len(labels) > 0 {
		w("    labels:\n")
		for _, k := range sortedKeys(labels) {
			w("      %s: %q\n", k, labels[k])
		}
	}
	switch {
	case network == "" || network == "bridge" || network == "podman":
		// default bridge: nothing to declare
	case network == "host" || network == "none" || strings.HasPrefix(network, "container:") || strings.HasPrefix(network, "ns:"):
		// not a named network: goes through network_mode verbatim
		w("    network_mode: %s\n", network)
	default:
		w("    networks:\n      %s:\n", network)
		if ip != "" {
			w("        ipv4_address: %s\n", ip)
		}
		w("\nnetworks:\n  %s:\n    external: true\n", network)
	}
	if len(envFile) == 0 {
		envFile["TZ"] = "UTC"
	}
	var env strings.Builder
	for _, k := range sortedKeys(envFile) {
		fmt.Fprintf(&env, "%s=%s\n", k, envFile[k])
	}
	return &Adopted{Service: svc, Compose: b.String(), Env: env.String(), Notes: notes}, nil
}

// ServiceName is the image's repository basename without tag or digest:
// "ghcr.io/daemonless/radarr:latest" -> "radarr".
func ServiceName(image string) string {
	base := path.Base(image)
	if i := strings.IndexByte(base, '@'); i >= 0 {
		base = base[:i]
	}
	if i := strings.IndexByte(base, ':'); i >= 0 {
		base = base[:i]
	}
	if base == "" {
		return "app"
	}
	return base
}

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = fmt.Sprintf("%q", s)
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
