package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/catalog"
	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/manifest"
)

// planIface is one row of a service's interface list in the install plan.
type planIface struct {
	Network string `json:"network"`
	IP      string `json:"ip,omitempty"`
	IP6     string `json:"ip6,omitempty"`
	MAC     string `json:"mac,omitempty"`
}

// installPlan is GET /api/apps/<id>/plan: a ready-to-submit installRequest
// seeded with this host's defaults (storage paths, folder sets, free ports,
// per-service networking, restart policy, public_url, and name), plus the
// UI notes that explain why a default was chosen.
type installPlan struct {
	installRequest
	DefaultedFrom map[string]string      `json:"defaultedFrom,omitempty"`
	PortNotes     map[string]string      `json:"portNotes,omitempty"`
	Interfaces    map[string][]planIface `json:"interfaces,omitempty"`
}

// handleAppRoutes dispatches /api/apps/<id>/<action>.
func (s *server) handleAppRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/apps/")
	appID, action, ok := strings.Cut(rest, "/")
	if !ok || appID == "" || action != "plan" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.handleAppPlan(w, r, appID)
}

// handleAppPlan answers GET /api/apps/<id>/plan[?engine=&catalog=&name=&appData=&choice=id=opt].
func (s *server) handleAppPlan(w http.ResponseWriter, r *http.Request, appID string) {
	if msg := s.namedEngineMissing(r); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	s.migrateSharedDefault(r.Context())

	q := r.URL.Query()
	srcID := strings.TrimSpace(q.Get("catalog"))
	entry, raw, err := s.cat.AppEntry(r.Context(), srcID, appID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	manifestText := string(raw)
	m, err := manifest.Parse(manifestText)
	if err != nil {
		http.Error(w, "manifest: "+err.Error(), http.StatusBadRequest)
		return
	}

	eng := strings.TrimSpace(q.Get("engine"))
	if eng == "" {
		eng = s.defaultEngine()
	}

	name := strings.TrimSpace(q.Get("name"))
	var slug string
	if name == "" {
		slug = s.manager.NextName(appID, appID)
		name = slug
	} else {
		slug = s.manager.NextName(name, appID)
	}

	locations := s.appDataLocations()
	base := chooseAppData(q.Get("appData"), locations)

	picks := map[string]string{}
	for _, c := range m.Choices {
		picks[c.ID] = c.Default
	}
	for _, kv := range q["choice"] {
		if id, opt, ok := strings.Cut(kv, "="); ok && id != "" && opt != "" {
			picks[id] = opt
		}
	}
	if s.usesDirectorBundles(eng) {
		for _, c := range m.Choices {
			for _, o := range c.Options {
				if o.ID == picks[c.ID] && strings.TrimSpace(o.Services) != "" && o.Appjail == nil {
					picks[c.ID] = c.Default
				}
			}
		}
	}

	values := map[string]string{}
	paths := map[string][]string{}
	defaultedFrom := map[string]string{}

	sets := loadSettings(s.fjordRoot).FolderSets
	if !s.pluginEnabled("homelab") {
		sets = nil
	}

	for _, v := range m.Variables {
		switch {
		case v.Type == "path":
			if fs := folderSetFor(v.Name, v.Label, sets); fs != nil {
				paths[v.Name] = slices.Clone(fs.Folders)
				values[v.Name] = fs.Folders[0]
				defaultedFrom[v.Name] = fs.Name
			} else {
				hint := pathHintFor(v.Name, base, slug)
				p := defaultPathFor(v.Default, appID, locations, base, slug, hint)
				paths[v.Name] = []string{p}
				values[v.Name] = p
			}
		case v.MadeUp() || (v.Type == "secret" && v.Given):
			values[v.Name] = ""
		case v.IsBool():
			on, off := v.BoolValues()
			if strings.EqualFold(v.Default, on) || strings.EqualFold(v.Default, "true") {
				values[v.Name] = on
			} else {
				values[v.Name] = off
			}
		default:
			values[v.Name] = v.Default
		}
	}

	for _, c := range m.Choices {
		pickID := picks[c.ID]
		for _, o := range c.Options {
			if o.ID != pickID {
				continue
			}
			for _, a := range o.Ask {
				values[a.Name] = askDefault(a, values[a.Name])
			}
		}
	}

	tag := defaultTagFor(entry)
	repo := entryImageRepo(entry)
	for _, v := range m.Variables {
		if v.Type == "image_tag" && v.Image != "" && v.Image == repo && tag != "" {
			values[v.Name] = tag
		}
	}
	if m.Class == "stack" || entry.Class == "stack" {
		tag = ""
	}

	// Check port variables against running containers, saved stacks, and host listeners.
	portNotes := map[string]string{}
	type portAsk struct {
		varName string
		raw     string
		num     int
		key     string
	}
	var portAsks []portAsk
	var rawPorts []string
	for _, v := range m.Variables {
		if v.Type != "port" {
			continue
		}
		val := strings.TrimSpace(values[v.Name])
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 || n > 65535 {
			continue
		}
		proto := portProto(manifestText, v.Name)
		key := fmt.Sprintf("%d/%s", n, proto)
		portAsks = append(portAsks, portAsk{varName: v.Name, raw: val, num: n, key: key})
		rawPorts = append(rawPorts, key)
	}
	if len(rawPorts) > 0 {
		pctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		taken := s.checkFreePorts(pctx, rawPorts)
		cancel()
		for _, pa := range portAsks {
			t, ok := taken[pa.key]
			if !ok {
				continue
			}
			if pa.num < 1024 {
				portNotes[pa.varName] = fmt.Sprintf("%s is in use by %s, and clients look for this app on %s: give it its own address on a LAN network (Networking, below).", pa.raw, t.HeldBy, pa.raw)
			} else if t.Free > 0 {
				values[pa.varName] = strconv.Itoa(t.Free)
				portNotes[pa.varName] = fmt.Sprintf("%s is in use by %s, so %d.", pa.raw, t.HeldBy, t.Free)
			} else {
				portNotes[pa.varName] = fmt.Sprintf("%s is in use by %s.", pa.raw, t.HeldBy)
			}
		}
	}

	// Default restart policy for the selected engine.
	var restart *composepkg.Restart
	st := loadSettings(s.fjordRoot)
	if be, ok := s.backend(eng); ok && len(be.Capabilities().RestartPolicies) > 0 {
		rp := defaultRestartFor(st, eng, be.Capabilities())
		restart = &rp
	}

	// Resolve default network and per-service interfaces.
	nets, _ := s.networksForEngine(r.Context(), eng)
	wantedNet := st.DefaultNetworkFor[eng]
	netChoice := ""
	if wantedNet != "" && (composepkg.BuiltIn(wantedNet) || slices.ContainsFunc(nets, func(n engine.Network) bool { return n.Name == wantedNet })) {
		netChoice = wantedNet
	}

	svcNames := m.ChosenServices(picks)
	hostNet := hostNetworkedManifest(manifestText)
	netPlan := map[string]string{}
	for _, svc := range svcNames {
		spec := m.Networking[svc]
		if spec == "" {
			spec = m.Networking["*"]
		}
		if spec != "" {
			netPlan[svc] = spec
		}
	}
	declaresNetworking := len(netPlan) > 0
	singleService := !declaresNetworking && len(svcNames) == 1
	perService := declaresNetworking || singleService

	var interfaces map[string][]planIface
	var reqNets []composepkg.Attachment
	var reqModes map[string]string
	if perService {
		if singleService {
			choice := netChoice
			if choice == "" {
				if hostNet {
					choice = composepkg.Host
				} else {
					choice = composepkg.Bridge
				}
			}
			interfaces = map[string][]planIface{
				svcNames[0]: {{Network: choice}},
			}
		} else {
			shipped := composepkg.Bridge
			if hostNet {
				shipped = composepkg.Host
			}
			resolved := resolveDefaultPlan(netPlan, nets, netChoice, shipped)
			interfaces = seedPlanInterfaces(svcNames, resolved)
		}
		reqNets, reqModes = splitPlanInterfaces(interfaces, svcNames)
	}

	// Fill public_url variables once port and network defaults are known.
	webNet := netChoice
	if perService {
		webNet = exposedNetwork(svcNames, netPlan, interfaces)
	}
	webOwn := isOwnAddressNetwork(webNet, nets)
	host := originHost(requestOrigin(r))
	for _, v := range m.Variables {
		if v.Type != "public_url" {
			continue
		}
		if u := suggestedPublicURL(m.WebPort, m.WebHTTPS, values, host, webOwn, "", manifestText); u != "" {
			values[v.Name] = u
		}
	}

	// Suggest a free name when a names provider is ready and this app has a web page.
	givenName := ""
	if m.WebPort != "" && m.Names == nil {
		nctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		ns := s.namesState(nctx)
		cancel()
		if ns.Ready {
			givenName = freeNameFor(nameLabel(slug), ns)
		}
	}

	stackNet := netChoice
	if perService {
		stackNet = ""
	}
	var choicesOut map[string]string
	if len(picks) > 0 {
		choicesOut = picks
	}

	plan := installPlan{
		installRequest: installRequest{
			Name:         name,
			AppID:        appID,
			Manifest:     manifestText,
			Values:       values,
			Paths:        paths,
			AppData:      base,
			Tag:          tag,
			Engine:       eng,
			Network:      stackNet,
			Networks:     reqNets,
			NetworkModes: reqModes,
			Choices:      choicesOut,
			GivenName:    givenName,
			Restart:      restart,
		},
		DefaultedFrom: defaultedFrom,
		PortNotes:     portNotes,
		Interfaces:    interfaces,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(plan)
}

var (
	nonAlnumRe      = regexp.MustCompile(`[^a-z0-9]+`)
	pathSuffixRe    = regexp.MustCompile(`[._]?path$`)
	wordSplitRe     = regexp.MustCompile(`[^A-Z0-9]+`)
	leadingVerTagRe = regexp.MustCompile(`^v?\d+(\.\d+)*-`)
	devTrainRe      = regexp.MustCompile(`(?i)(^|-)(devel|dev|edge|nightly|rc|beta|alpha)(-|$)`)
	networkModeRe   = regexp.MustCompile(`(?m)^\s*network_mode\s*:`)
	portVarRefRe    = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)`)
	digitsPortRe    = regexp.MustCompile(`^\d{2,5}$`)
)

// usesDirectorBundles reports whether eng orchestrates stacks through AppJail
// director bundles rather than compose services directly.
func (s *server) usesDirectorBundles(eng string) bool {
	if be, ok := s.backend(eng); ok && be.Capabilities().DirectorBundles {
		return true
	}
	if d, ok := descriptor(eng); ok && d.DirectorBundles {
		return true
	}
	return eng != "" && eng != "podman"
}

// pathHintFor mirrors the wizard's pathHint:
// <base>/<slug>/<var-lowercased-sans-suffix>.
func pathHintFor(varName, base, slug string) string {
	sub := pathSuffixRe.ReplaceAllString(strings.ToLower(varName), "")
	sub = strings.Trim(nonAlnumRe.ReplaceAllString(sub, "-"), "-")
	if sub == "" {
		sub = "data"
	}
	return strings.TrimRight(base, "/") + "/" + slug + "/" + sub
}

// defaultPathFor mirrors ui/src/pathDefaults.ts: re-roots a catalog default
// under <root>/<appID> to <base>/<slug>, or returns hint when def is empty.
func defaultPathFor(def, appID string, locations []string, base, slug, hint string) string {
	d := strings.TrimSpace(def)
	if d == "" {
		return hint
	}
	if appID == "" {
		return d
	}
	seen := map[string]bool{}
	var roots []string
	for _, r := range append(slices.Clone(locations), base, "/containers") {
		r = strings.TrimRight(strings.TrimSpace(r), "/")
		if r != "" && !seen[r] {
			seen[r] = true
			roots = append(roots, r)
		}
	}
	cleanBase := strings.TrimRight(base, "/")
	for _, root := range roots {
		own := root + "/" + appID
		if d == own {
			return cleanBase + "/" + slug
		}
		if rest, ok := strings.CutPrefix(d, own+"/"); ok {
			return cleanBase + "/" + slug + "/" + rest
		}
	}
	return d
}

// folderSetFor mirrors ui/src/folderMatch.ts: returns the first folder set with
// folders whose Match keywords hit a whole word of name (or of label when label
// contains LIBRARY).
func folderSetFor(name, label string, sets []FolderSet) *FolderSet {
	byName := splitWords(name)
	var byLabel []string
	if label != "" {
		byLabel = splitWords(label)
	}
	for i := range sets {
		if sets[i].Match != "" && len(sets[i].Folders) > 0 && wordsHit(byName, sets[i].Match) {
			return &sets[i]
		}
	}
	if slices.Contains(byLabel, "LIBRARY") {
		for i := range sets {
			if sets[i].Match != "" && len(sets[i].Folders) > 0 && wordsHit(byLabel, sets[i].Match) {
				return &sets[i]
			}
		}
	}
	return nil
}

func splitWords(s string) []string {
	var out []string
	for _, w := range wordSplitRe.Split(strings.ToUpper(s), -1) {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

func wordsHit(ws []string, match string) bool {
	var keys []string
	for _, k := range strings.Split(match, "|") {
		k = strings.TrimSpace(strings.ToUpper(k))
		if k != "" {
			keys = append(keys, k)
		}
	}
	for _, w := range ws {
		for _, k := range keys {
			if w == k || w == k+"S" {
				return true
			}
		}
	}
	return false
}

// askDefault mirrors ui/src/choices.ts askValue: a values map with one entry
// selects that entry automatically; otherwise current or a.Default.
func askDefault(a manifest.Ask, cur string) string {
	if len(a.Values) == 1 {
		for k := range a.Values {
			return k
		}
	}
	if cur != "" {
		return cur
	}
	return a.Default
}

// defaultTagFor picks the default train/tag from a catalog entry's variants using
// the same precedence as InstallWizard.svelte.
func defaultTagFor(entry catalog.Entry) string {
	variants := entry.Variants
	if len(variants) == 0 {
		return ""
	}
	has := func(id string) bool {
		for _, v := range variants {
			if v.ID == id {
				return true
			}
		}
		return false
	}
	if has("latest") {
		return "latest"
	}
	def := ""
	for _, v := range variants {
		if v.Default {
			def = v.ID
			break
		}
	}
	if def != "" && has(def) {
		return def
	}
	if def != "" {
		stripped := leadingVerTagRe.ReplaceAllString(def, "")
		if has(stripped) {
			return stripped
		}
	}
	for _, cand := range []string{"pkg-latest", "pkg"} {
		if has(cand) {
			return cand
		}
	}
	for _, v := range variants {
		if !devTrainRe.MatchString(v.ID) {
			return v.ID
		}
	}
	return variants[0].ID
}

// entryImageRepo strips the tag from the first variant's image reference.
func entryImageRepo(entry catalog.Entry) string {
	if len(entry.Variants) == 0 {
		return ""
	}
	img := entry.Variants[0].Image
	slash := strings.LastIndex(img, "/")
	colon := strings.LastIndex(img, ":")
	if colon > slash {
		return img[:colon]
	}
	return img
}

// portProto reports whether manifestText maps ${varName} to a UDP container port.
func portProto(manifestText, varName string) string {
	re := regexp.MustCompile(`\$\{` + regexp.QuoteMeta(varName) + `\}:[0-9]+/udp`)
	if re.MatchString(manifestText) {
		return "udp"
	}
	return "tcp"
}

func hostNetworkedManifest(manifestText string) bool {
	return networkModeRe.MatchString(manifestText)
}

// joinableNetworks filters out per-stack private networks and engine-allocated
// internal networks, matching ui/src/planSeed.ts joinable().
func joinableNetworks(nets []engine.Network) []engine.Network {
	var out []engine.Network
	for _, n := range nets {
		if !n.Private && n.AddressSource != "engine" {
			out = append(out, n)
		}
	}
	return out
}

// resolveDefaultPlan mirrors ui/src/planSeed.ts resolveDefault().
func resolveDefaultPlan(plan map[string]string, nets []engine.Network, configured, shipped string) map[string]string {
	hasDefault := false
	for _, v := range plan {
		if v == manifest.NetworkDefault {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		return plan
	}
	var real []string
	for _, n := range joinableNetworks(nets) {
		real = append(real, n.Name)
	}
	builtin := composepkg.BuiltIn(configured)
	pick := ""
	if !builtin {
		if slices.Contains(real, configured) {
			pick = configured
		} else if len(real) > 0 {
			pick = real[0]
		}
	}
	out := make(map[string]string, len(plan))
	if pick == "" {
		for k := range plan {
			out[k] = shipped
		}
		return out
	}
	for k, v := range plan {
		if v == manifest.NetworkDefault {
			out[k] = pick
		} else {
			out[k] = v
		}
	}
	return out
}

// seedPlanInterfaces mirrors ui/src/planSeed.ts seedInterfaces().
func seedPlanInterfaces(services []string, plan map[string]string) map[string][]planIface {
	out := make(map[string][]planIface, len(services))
	anyPrivate := false
	for _, svc := range services {
		spec := plan[svc]
		if spec == "" {
			spec = manifest.NetworkPrivate
		}
		if spec == manifest.NetworkPrivate {
			anyPrivate = true
			break
		}
	}
	for _, svc := range services {
		spec := plan[svc]
		if spec == "" {
			spec = manifest.NetworkPrivate
		}
		switch {
		case composepkg.BuiltIn(spec):
			out[svc] = []planIface{{Network: spec}}
		case spec == manifest.NetworkPrivate:
			out[svc] = []planIface{{Network: manifest.NetworkPrivate}}
		case len(services) == 1 || !anyPrivate:
			out[svc] = []planIface{{Network: spec}}
		default:
			out[svc] = []planIface{
				{Network: spec},
				{Network: manifest.NetworkPrivate},
			}
		}
	}
	return out
}

// splitPlanInterfaces mirrors ui/src/planSeed.ts splitPlan() in deterministic
// service order.
func splitPlanInterfaces(edits map[string][]planIface, services []string) ([]composepkg.Attachment, map[string]string) {
	var networks []composepkg.Attachment
	modes := map[string]string{}
	for _, svc := range services {
		rows := edits[svc]
		placed := false
		alone := len(rows) == 1
		for _, r := range rows {
			net := r.Network
			if net == "" {
				net = composepkg.Bridge
			}
			placed = true
			if net == composepkg.Host || net == composepkg.None || (net == composepkg.Bridge && alone) {
				modes[svc] = net
				continue
			}
			networks = append(networks, composepkg.Attachment{
				Network: net,
				Service: svc,
				IP:      strings.TrimSpace(r.IP),
				IP6:     strings.TrimSpace(r.IP6),
				MAC:     strings.TrimSpace(r.MAC),
			})
		}
		if !placed {
			modes[svc] = composepkg.None
		}
	}
	if len(modes) == 0 {
		modes = nil
	}
	return networks, modes
}

func exposedNetwork(services []string, netPlan map[string]string, interfaces map[string][]planIface) string {
	for _, svc := range services {
		if netPlan[svc] == manifest.NetworkDefault && len(interfaces[svc]) > 0 {
			return interfaces[svc][0].Network
		}
	}
	if len(services) > 0 && len(interfaces[services[0]]) > 0 {
		return interfaces[services[0]][0].Network
	}
	return ""
}

func isOwnAddressNetwork(netName string, nets []engine.Network) bool {
	if netName == "" {
		return false
	}
	for _, n := range nets {
		if n.Name == netName && (n.Driver == "epair" || n.Driver == "macvlan") {
			return true
		}
	}
	return false
}

// insidePort extracts the container port from a "${NAME}:N" mapping in composeYAML.
func insidePort(composeYAML, varName string) string {
	re := regexp.MustCompile(`\$\{` + regexp.QuoteMeta(varName) + `(?:[:-][^}]*)?\}:(\d{2,5})`)
	if m := re.FindStringSubmatch(composeYAML); m != nil {
		return m[1]
	}
	return ""
}

// suggestedPublicURL mirrors ui/src/publicUrl.ts suggestedPublicUrl().
func suggestedPublicURL(webPort string, https bool, values map[string]string, host string, ownAddr bool, ownIP, composeYAML string) string {
	scheme := "http"
	if https {
		scheme = "https"
	}
	hostPort := webPort
	portVar := ""
	if m := portVarRefRe.FindStringSubmatch(webPort); m != nil {
		portVar = m[1]
		hostPort = values[portVar]
	}
	if ownAddr {
		inPort := ""
		if portVar != "" {
			inPort = insidePort(composeYAML, portVar)
		}
		port := inPort
		if port == "" {
			port = hostPort
		}
		if ownIP != "" && port != "" {
			return fmt.Sprintf("%s://%s:%s", scheme, ownIP, port)
		}
		return ""
	}
	if !digitsPortRe.MatchString(hostPort) || host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s:%s", scheme, host, hostPort)
}

// freeNameFor mirrors ui/src/names.ts freeName(): returns base, or base-2,
// base-3... until a label not owned by any stack on this fjord or taken on the
// provider is found.
func freeNameFor(base string, ns namesView) string {
	if base == "" {
		return ""
	}
	taken := func(label string) bool {
		for _, h := range ns.Names {
			if h.Name == label {
				return true
			}
		}
		return slices.Contains(ns.Taken, label)
	}
	l := base
	for n := 2; taken(l); n++ {
		l = fmt.Sprintf("%s-%d", base, n)
	}
	return l
}
