package main

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/manifest"
)

// privateNetName bounds a network name. appjail makes the bridge with this
// name, and an interface name must fit IFNAMSIZ -- 16 including the NUL, so 15
// characters. Measured, not assumed: 15 is accepted and 16 is refused with
// "network name too long".
const privateNetNameMax = 15

// privateNetSuffix marks a stack's own segment. Short on purpose: it has to
// leave room for the stack's name inside privateNetNameMax, and "_private"
// left only seven characters, which "immich-2" already overflows.
const privateNetSuffix = "_priv"

// privateNetworkName is what a stack's own private segment is called.
//
// Named after the stack so a sorted list keeps a stack's things together, and
// so the name says whose it is when it turns up attached to something.
func privateNetworkName(stackID string) string {
	clean := regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(stackID, "-")
	clean = strings.Trim(clean, "-._")
	if max := privateNetNameMax - len(privateNetSuffix); len(clean) > max {
		clean = strings.TrimRight(clean[:max], "-._")
	}
	if clean == "" {
		clean = "stack"
	}
	return clean + privateNetSuffix
}

// planServiceNetworks turns a manifest's networking declaration into one
// attachment per service.
//
// The declaration is the app's own knowledge -- immich knows immich-server is
// the one people open and that its postgres is not -- so an install asks only
// which network the app should be on, and everything else follows. Before
// this, putting immich on a network put its database on the same one.
//
// chosen is what the install was told to use, and stands in for "default".
// Services asking for "private" get a segment this stack owns, created on
// demand. A spec that is neither is a network name, used as it stands.
func planServiceNetworks(
	ctx context.Context,
	plan map[string]string,
	chosen []composepkg.Attachment,
	services []string,
	ensurePrivate func() (string, error),
) ([]composepkg.Attachment, map[string]string, error) {
	// "private" as a network name in an attachment: the caller listed the
	// interfaces itself and used the spec, because the segment has no name
	// until this install makes one. Resolved FIRST, before any early return:
	// an app with no networking: block takes the plan-less path below, and the
	// literal string would have gone to the engine as a network name.
	needsPrivate := false
	for _, a := range chosen {
		if a.Network == manifest.NetworkPrivate {
			needsPrivate = true
		}
	}
	if needsPrivate {
		name, err := ensurePrivate()
		if err != nil {
			return nil, nil, err
		}
		for i := range chosen {
			if chosen[i].Network == manifest.NetworkPrivate {
				chosen[i].Network = name
			}
		}
	}
	// No plan: the caller enumerated the interfaces itself -- the install
	// wizard's per-service editor sends its rows and no plan at all -- so the
	// list is taken as it stands. Re-deriving it from a plan would overwrite
	// what was on screen, which is what made deleting an interface look
	// broken. The map is empty rather than nil because the caller's own
	// per-service modes are merged into it.
	if len(plan) == 0 || len(services) == 0 {
		return chosen, map[string]string{}, nil
	}

	// Addresses the caller gave per service. The PLAN still decides which
	// network each one lands on -- a caller that sent only the services it put
	// on a real network would otherwise drop every "private" row on the floor.
	addr := map[string]composepkg.Attachment{}
	var stackWide []composepkg.Attachment
	for _, a := range chosen {
		if a.Service != "" {
			addr[a.Service] = a
			continue
		}
		stackWide = append(stackWide, a)
	}
	private := ""
	modes := map[string]string{}
	var out []composepkg.Attachment
	for _, svc := range services {
		spec, ok := plan[svc]
		if !ok {
			spec, ok = plan[manifest.NetworkEveryOther]
		}
		if !ok {
			continue // named by nothing: left alone
		}
		switch spec {
		case manifest.NetworkDefault:
			for _, a := range stackWide {
				a.Service = svc
				if p, ok := addr[svc]; ok {
					a.IP, a.MAC = p.IP, p.MAC
				}
				out = append(out, a)
			}
		case manifest.NetworkPrivate:
			if private == "" {
				name, err := ensurePrivate()
				if err != nil {
					return nil, nil, err
				}
				private = name
			}
			out = append(out, composepkg.Attachment{Network: private, Service: svc})
		case composepkg.Host, composepkg.None, composepkg.Bridge:
			// A built-in is a mode on the service, not a network to join.
			modes[svc] = spec
		case "":
			// Deliberately on nothing.
		default:
			a := composepkg.Attachment{Network: spec, Service: svc}
			if p, ok := addr[svc]; ok {
				a.IP, a.MAC = p.IP, p.MAC
			}
			out = append(out, a)
		}
	}
	// A plan naming services this stack does not have is a typo worth saying
	// out loud: silently it just means some service got nothing.
	have := map[string]bool{manifest.NetworkEveryOther: true}
	for _, s := range services {
		have[s] = true
	}
	var unknown []string
	for name := range plan {
		if !have[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, nil, fmt.Errorf("this app's networking names %s, which %s not services it has (%s)",
			strings.Join(unknown, ", "),
			map[bool]string{true: "is", false: "are"}[len(unknown) == 1],
			strings.Join(services, ", "))
	}
	// A service on a real network still has to reach the rest of its own
	// stack. immich-server on the LAN with its database on a private segment
	// cannot see postgres at all unless it is on that segment too -- so it
	// joins both, and nobody has to be told to ask for it.
	//
	// Only when a private segment exists at all, and never for a service that
	// is already on it.
	if private != "" {
		for i := range out {
			if out[i].Network == private {
				continue
			}
			already := false
			for _, a := range out {
				if a.Service == out[i].Service && a.Network == private {
					already = true
					break
				}
			}
			if !already {
				out = append(out, composepkg.Attachment{Network: private, Service: out[i].Service})
			}
		}
	}
	return out, modes, nil
}

// privateOnlyServices names the services whose every interface is on the
// stack's own private segment -- the ones nothing off this host can reach, and
// so the ones that still have to publish their ports.
func privateOnlyServices(atts []composepkg.Attachment, private string) []string {
	if private == "" {
		return nil
	}
	elsewhere := map[string]bool{}
	onPrivate := map[string]bool{}
	for _, a := range atts {
		if a.Service == "" {
			continue
		}
		if a.Network == private {
			onPrivate[a.Service] = true
			continue
		}
		elsewhere[a.Service] = true
	}
	var out []string
	for svc := range onPrivate {
		if !elsewhere[svc] {
			out = append(out, svc)
		}
	}
	sort.Strings(out)
	return out
}

// ensurePrivateNetwork returns the name of this stack's private segment,
// creating it if it is not there yet.
//
// "Private" means the ENGINE allocates on it: it makes the bridge, hands out
// the addresses, and nothing on the LAN can reach what is on it. That is the
// engine's own network kind, so this asks the engine rather than writing a
// conflist -- and it is the same property the Networks page folds these away
// by, so a stack's private segment never clutters it.
func (s *server) ensurePrivateNetwork(ctx context.Context, engineName, stackID string) (string, error) {
	name := privateNetworkName(stackID)
	if _, ok := hostnet.Get(name); ok {
		return name, nil
	}
	backend, ok := s.backend(engineName)
	if !ok {
		backend, ok = s.backend(s.defaultEngine())
	}
	if !ok {
		return "", fmt.Errorf("no engine to create %s on", name)
	}
	for _, n := range backend.Capabilities().NetworkKinds {
		if n.ID != "nat" {
			continue
		}
		// The engine's own networks too: appjail's virtualnets are not
		// conflists, and their bridge exists only while a jail is up -- so
		// neither hostnet nor the host's interfaces know about them, and a
		// second stack was handed a segment the first already owned.
		var engineNets []engine.Network
		if ns, err := backend.Networks(ctx); err == nil {
			engineNets = ns
		}
		subnet, err := freePrivateSubnet(engineNets)
		if err != nil {
			return "", err
		}
		if _, err := backend.CreateNetwork(ctx, engine.NetworkSpec{
			Name: name, Kind: n.ID, Subnet: subnet,
		}); err != nil {
			// Someone else may have made it between the check and here.
			if _, there := hostnet.Get(name); there {
				return name, nil
			}
			return "", fmt.Errorf("create %s: %w", name, err)
		}
		return name, nil
	}
	return "", fmt.Errorf("%s cannot make a private network on this host", engineName)
}

// privateSubnetBase is where fjord looks for a segment to give a stack.
//
// Deliberately NOT 10.88/16: that is podman's own default network, and a
// private segment overlapping it takes the host's containers down with it.
// 172.16/12 is docker's. 10.100+ is left alone by both.
const (
	privateSubnetFirst = 100
	privateSubnetLast  = 199
)

// freePrivateSubnet picks a /24 nothing on this host is using.
//
// "Nothing" means neither an address the host itself holds nor a network fjord
// already knows about -- a stack whose database answers on a segment something
// else is also on is the kind of fault that looks like the app misbehaving.
func freePrivateSubnet(engineNets []engine.Network) (string, error) {
	taken := hostSubnets()
	for _, n := range engineNets {
		if n.Subnet == "" {
			continue
		}
		if _, ipnet, err := net.ParseCIDR(n.Subnet); err == nil {
			taken = append(taken, ipnet)
		}
	}
	for i := privateSubnetFirst; i <= privateSubnetLast; i++ {
		cidr := fmt.Sprintf("10.%d.0.0/24", i)
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if !overlapsAny(ipnet, taken) {
			return cidr, nil
		}
	}
	return "", fmt.Errorf("no free private segment between 10.%d.0.0/24 and 10.%d.0.0/24 -- every one is already in use on this host",
		privateSubnetFirst, privateSubnetLast)
}

// hostSubnets is every segment this host is on: its own interface addresses
// and the networks fjord has defined.
func hostSubnets() []*net.IPNet {
	var out []*net.IPNet
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() {
				out = append(out, ipnet)
			}
		}
	}
	for _, d := range hostnet.List() {
		if d.Subnet == "" {
			continue
		}
		if _, ipnet, err := net.ParseCIDR(d.Subnet); err == nil {
			out = append(out, ipnet)
		}
	}
	return out
}

// overlapsAny reports whether cidr shares any address with one of nets.
func overlapsAny(cidr *net.IPNet, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(cidr.IP) || cidr.Contains(n.IP) {
			return true
		}
	}
	return false
}

// serviceHostnames is the env the stack needs so its parts can find each
// other: the variable a service's consumers read, set to that service's name.
//
// With container DNS a service answers at its own name on any network it
// shares with the reader, so the NAME is the whole answer -- no address to
// pin, allocate, or write back after something starts. Without DNS this is
// wrong, which is why fjord's doctor checks for it.
//
// Only for services that actually got a network. One left on the host's stack
// is reached at localhost, which is what the bundle already defaults to.
func serviceHostnames(hostnames map[string]string, atts []composepkg.Attachment, modes map[string]string) map[string]string {
	if len(hostnames) == 0 {
		return nil
	}
	networked := map[string]bool{}
	for _, a := range atts {
		if a.Service != "" && a.Network != "" {
			networked[a.Service] = true
		}
	}
	out := map[string]string{}
	for svc, v := range hostnames {
		if v == "" || !networked[svc] {
			continue
		}
		// A mode is not a network: a service on host or none has no name for
		// anyone to resolve.
		if m := modes[svc]; m != "" {
			continue
		}
		out[v] = svc
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// appHostnames is the service -> env-var map the app that this stack was
// installed from declares, or nil when there is none to be had.
//
// A stack records the catalog app it came from, not the manifest, so this asks
// the catalog again. Best-effort by design: a catalog that has since gone away
// must not make saving a stack fail -- it only means the variables are left
// exactly as they are.
func (s *server) appHostnames(ctx context.Context, stackName string) map[string]string {
	st, err := s.manager.Get(stackName)
	if err != nil || st.State == nil || st.State.Origin.AppID == "" {
		return nil
	}
	b, err := s.cat.ManifestAny(ctx, st.State.Origin.AppID)
	if err != nil {
		return nil
	}
	m, err := manifest.Parse(string(b))
	if err != nil {
		return nil
	}
	return m.Hostnames
}

// rewriteHostnames sets each declared variable to the name of the service it
// points at, in an existing .env, and leaves everything else untouched.
//
// The stack ships addressed over localhost, which is true only while its parts
// share one network stack. Moving one onto a network of its own makes
// localhost meaningless, and the install path already rewrites these -- save
// did not, so taking immich off the host's stack left DB_HOSTNAME=localhost
// and the server came up unable to reach a database that was running fine.
func rewriteHostnames(env string, hostnames map[string]string, atts []composepkg.Attachment, modes map[string]string) string {
	want := serviceHostnames(hostnames, atts, modes)
	if len(want) == 0 {
		return env
	}
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(env, "\n") {
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			out = append(out, line)
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if v, ok := want[key]; ok {
			out = append(out, key+"="+v)
			seen[key] = true
			continue
		}
		out = append(out, line)
	}
	// A variable the app declares but the .env never carried still has to be
	// set, or the compose falls back to its :-localhost default.
	var missing []string
	for k, v := range want {
		if !seen[k] {
			missing = append(missing, k+"="+v)
		}
	}
	sort.Strings(missing)
	if len(missing) == 0 {
		return strings.Join(out, "\n")
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	out = append(out, missing...)
	return strings.Join(out, "\n") + "\n"
}

// envMap reads a .env's KEY=VALUE lines, for the callers that hold the text
// rather than the map the install pipeline builds.
func envMap(env string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(env, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if eq := strings.IndexByte(line, '='); eq > 0 {
			out[strings.TrimSpace(line[:eq])] = strings.TrimSpace(line[eq+1:])
		}
	}
	return out
}
