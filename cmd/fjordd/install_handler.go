package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/stack"
)

// storageBase is the default "App data" folder: every app gets
// "<base>/<slug>/<name>". It's the first configured location (Settings), else
// FJORD_STORAGE_BASE, else <fjordRoot>/containers. Older installs that never
// set it kept data under <fjordRoot>/volumes; stacks record absolute paths, so
// nothing moves.
func (s *server) storageBase() string {
	if l := loadSettings(s.fjordRoot).AppData; len(l) > 0 {
		return l[0]
	}
	if b := os.Getenv("FJORD_STORAGE_BASE"); b != "" {
		return b
	}
	return filepath.Join(s.fjordRoot, "containers")
}

// appDataLocations is the ordered list of App data folders an install may
// choose from; the first is the default. One entry when nothing is configured.
func (s *server) appDataLocations() []string {
	if l := loadSettings(s.fjordRoot).AppData; len(l) > 0 {
		return l
	}
	return []string{s.storageBase()}
}

// chooseAppData returns the requested App data folder if it's one of the
// configured locations, else the default (first). The wizard can't point an
// install at an arbitrary path through this field.
func chooseAppData(requested string, locations []string) string {
	requested = strings.TrimSpace(requested)
	for _, l := range locations {
		if requested != "" && requested == l {
			return l
		}
	}
	return locations[0]
}

// storageSlug is the App data folder name for a stack: its display name
// slugged, the id as fallback (see stack.Slug).
func storageSlug(name, id string) string { return stack.Slug(name, id) }

// installRequest is the /api/apps/install payload: a catalog manifest plus the
// wizard's variable values and an optional macvlan attachment.
// attachments is the request's networks in list form: the multi-network field
// when it is set, else the single Network/IP/MAC triple. One shape reaches the
// injector, so an older client that only knows the triple still works.
func (r installRequest) attachments() []composepkg.Attachment {
	if len(r.Networks) > 0 {
		return r.Networks
	}
	if r.Network == "" || composepkg.BuiltIn(r.Network) {
		return nil
	}
	return []composepkg.Attachment{{Network: r.Network, IP: r.IP, MAC: r.MAC}}
}

// What an install request asks for on the network.
const (
	netActionNothing = ""
	netActionNone    = "none"
	netActionHost    = "host"
	netActionAttach  = "attach"
)

// networkAction decides which of the three a request means.
//
// A per-service list is the whole answer and outranks the stack-wide
// Network/IP/MAC fields, which are the single-network form. The wizard sends
// its chosen network alongside the per-service rows, and that choice can be a
// built-in: read in the other order, HostNetwork() put every service on the
// host's stack, discarded the whole plan, and reported success -- which is
// what "per service didn't save" was.
func (r installRequest) networkAction() string {
	if len(r.Networks) > 0 {
		return netActionAttach
	}
	switch r.Network {
	case composepkg.None:
		return netActionNone
	case composepkg.Host:
		return netActionHost
	}
	// Modes with no interfaces at all is a real answer -- it is what the
	// wizard sends for a stack kept on the arrangement it ships with -- and
	// falling through left SetServiceModes unreached, so the choice was
	// silently dropped for any app not already in that mode.
	if len(r.attachments()) > 0 || len(r.NetworkModes) > 0 {
		return netActionAttach
	}
	return netActionNothing
}

// installNetworkPlan is the service -> spec map this install should apply, or
// nil when the request already answered per service.
//
// The manifest's own declaration is the default, which is what makes a
// one-click install land correctly. But a request carrying a per-service
// interface list has ALREADY had it applied -- the wizard resolved it on
// screen and the operator then edited it -- and re-applying it here threw
// those rows away: immich-server's spec is "default", which matches only a
// stack-wide network, so the service people open ended up on no network at
// all while the rest of the stack moved to the private segment.
func installNetworkPlan(req installRequest, declared map[string]string) map[string]string {
	if len(req.Networks) > 0 {
		return nil
	}
	if req.NetworkPlan != nil {
		return req.NetworkPlan
	}
	return declared
}

type installRequest struct {
	Name     string            `json:"name"`
	AppID    string            `json:"app_id,omitempty"` // catalog app id, for icon/link resolution
	Manifest string            `json:"manifest"`         // full x-fjord manifest YAML
	Values   map[string]string `json:"values"`
	// Paths carries the wizard's folder list per host-path variable. One entry
	// is the plain value; several become extra binds under the app's own mount
	// point (see applyPathLists). Values still wins when Paths is absent.
	Paths   map[string][]string `json:"paths,omitempty"`
	AppData string              `json:"appData,omitempty"` // chosen App data folder; must be a configured location
	Tag     string              `json:"tag,omitempty"`     // image tag/variant to deploy
	Engine  string              `json:"engine,omitempty"`  // runtime to install on; "" = default
	Network string              `json:"network,omitempty"`
	IP      string              `json:"ip,omitempty"`
	MAC     string              `json:"mac,omitempty"` // pin a MAC so a DHCP reservation resolves
	// Networks is the full list when a stack takes more than one interface.
	// Network/IP/MAC above remain the single-network form.
	Networks []composepkg.Attachment `json:"networks,omitempty"`
	// NetworkPlan overrides the app's own service -> network-spec map, so the
	// wizard can show what the app suggests and let it be changed before
	// install. The specs are resolved here, not there: "private" names a
	// segment that does not exist until this install makes it.
	NetworkPlan map[string]string `json:"networkPlan,omitempty"`
	// NetworkModes is service -> built-in (host/bridge/none). A mode is set on
	// the service rather than joined, so it cannot travel as an attachment.
	NetworkModes map[string]string `json:"networkModes,omitempty"`
}

// handleInstall installs a catalog app end-to-end: resolve wizard input,
// provision host dirs, render the stack (compose + .env), optionally give it
// its own IP, and bring it up -- streaming the output back like up does.
func (s *server) handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req installRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	// req.Name is the friendly DISPLAY name the user typed; the stack's stable
	// identity (dir, compose project, containers, volume paths) is a fresh
	// numeric id, so two installs of the same app can share a display name and
	// a later rename is a metadata-only edit.
	if !stack.ValidDisplayName(req.Name) {
		http.Error(w, "invalid name: letters, digits, spaces, '.', '_' and '-' only (max 64)", 400)
		return
	}
	// Values land in .env as KEY=VALUE lines: no line breaks or control chars.
	for k, v := range req.Values {
		if hasControlChars(k) || hasControlChars(v) {
			http.Error(w, "value for "+k+" contains control characters", 400)
			return
		}
	}
	for k, list := range req.Paths {
		for _, p := range list {
			if hasControlChars(k) || hasControlChars(p) {
				http.Error(w, "folder for "+k+" contains control characters", 400)
				return
			}
		}
	}
	id := s.manager.AllocateName(req.Name, req.AppID)
	m, err := manifest.Parse(req.Manifest)
	if err != nil {
		http.Error(w, "manifest: "+err.Error(), 400)
		return
	}
	if req.Values == nil {
		req.Values = map[string]string{}
	}
	// Remote folders (nfs:// / smb:// URLs) in a path list become named
	// volumes attached after the compose is rendered; host paths go through
	// the usual variable + multi-folder path. With one remote next to host
	// folders, everything lands as sub-folders of the mount point.
	remotes := map[string][]remoteFolder{}
	for name, list := range req.Paths {
		var locals []string
		for _, p := range list {
			if rf, ok := parseRemote(p); ok {
				remotes[name] = append(remotes[name], rf)
			} else if strings.TrimSpace(p) != "" {
				locals = append(locals, p)
			}
		}
		req.Paths[name] = locals
	}
	pathExpansions := applyPathLists(req.Values, req.Paths)
	for _, v := range m.Variables {
		if v.Type == "path" && v.Optional != true && len(remotes[v.Name]) > 0 && len(req.Paths[v.Name]) == 0 {
			http.Error(w, v.Name+" needs at least one host folder; remote folders mount alongside it", 400)
			return
		}
	}
	// Host paths may use {{stack}} / {{base}} (typed, or from a folder set):
	// resolve them now so Resolve sees concrete absolute paths, and queue the
	// ones that land inside the storage base -- this stack's own dirs -- for
	// provisioning alongside managed storage.
	slug, base := storageSlug(req.Name, id), chooseAppData(req.AppData, s.appDataLocations())
	pathVars := map[string]bool{}
	for _, v := range m.Variables {
		if v.Type == "path" {
			pathVars[v.Name] = true
		}
	}
	perStackDirs := expandPathTemplates(req.Values, pathExpansions, pathVars, slug, base)

	// Managed storage lives at <base>/<slug>/... -- a human, discoverable path
	// (default /containers/tautulli/config, matching the common host convention)
	// rather than an opaque numeric-id dir. The slug comes from the display name;
	// a pre-existing dir is adopted, so migrating in place is just naming the
	// stack to match. Identity stays the numeric id; this path never moves on a
	// later rename.
	res, err := m.Resolve(req.Values, slug, base)
	if err != nil {
		http.Error(w, "invalid input: "+err.Error(), 400)
		return
	}
	res.Dirs = append(res.Dirs, perStackDirs...)
	if err := manifest.Provision(res.Dirs); err != nil {
		http.Error(w, "provision: "+err.Error(), 500)
		return
	}
	composeYAML, err := composepkg.DropVolumesReferencing(m.Compose(), res.EmptyOptional)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Optional ports left blank (an unused HTTPS listener) go the same way.
	composeYAML, err = composepkg.DropPortsReferencing(composeYAML, res.EmptyOptional)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if req.Tag != "" {
		composeYAML, err = composepkg.SetImageTag(composeYAML, req.Tag)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	// Built-ins chosen per service, carried to the appjail bundle below:
	// the compose is not where an appjail stack's networking lives.
	svcModes := map[string]string{}
	var plannedAtts []composepkg.Attachment
	// The built-ins are states rather than networks to attach to, so
	// attachments() is empty for them and nothing here used to act on one: an
	// install asking for "none" quietly landed on the bridge instead.
	perSvcList := len(req.Networks) > 0
	switch req.networkAction() {
	case netActionNone:
		composeYAML, err = composepkg.DisableNetwork(composeYAML)
	case netActionHost:
		composeYAML, err = composepkg.HostNetwork(composeYAML)
	case netActionAttach:
		// Only when a stack-level network was named. An install that lists its
		// interfaces per service sends none, and checking the empty string
		// refused the install with `no network named ""`.
		if !perSvcList && req.Network != "" {
			if msg := s.networkUnusable(r.Context(), req.Engine, req.Network); msg != "" {
				http.Error(w, msg, 400)
				return
			}
		}
		// Specs are not networks: "private" names a segment this install
		// creates, so it cannot be looked up before it exists.
		var declared []composepkg.Attachment
		for _, a := range req.attachments() {
			if a.Network != "" && a.Network != manifest.NetworkPrivate {
				declared = append(declared, a)
			}
		}
		if msg := attachmentsUnusable(declared, s.engineNetworks(r.Context(), req.Engine)); msg != "" {
			http.Error(w, msg, 400)
			return
		}
		// The app says which of its services is the one people open and which
		// are its database and cache. Without that, choosing a network for
		// immich put its postgres on the LAN too.
		var names []string
		for _, svc := range composepkg.ParseServices(composeYAML, res.Env) {
			names = append(names, svc.Name)
		}
		plan := installNetworkPlan(req, m.Networking)
		atts, modes, planErr := planServiceNetworks(r.Context(), plan, req.attachments(), names,
			func() (string, error) { return s.ensurePrivateNetwork(r.Context(), req.Engine, id) })
		if planErr != nil {
			http.Error(w, "network: "+planErr.Error(), 400)
			return
		}
		if len(atts) > 0 {
			composeYAML, err = composepkg.InjectNetworks(composeYAML, atts)
		}
		// Services asking for a built-in are modes, not attachments, and are
		// set after: InjectNetworks clears the mode of anything it attaches.
		for svc, m := range req.NetworkModes {
			modes[svc] = m
		}
		svcModes, plannedAtts = modes, atts
		if err == nil && len(modes) > 0 {
			composeYAML, err = composepkg.SetServiceModes(composeYAML, modes)
		}
		// A service that ended up ONLY on this stack's private segment keeps
		// its published ports. Attaching stashes them because a container with
		// its own address on a real segment is the endpoint -- but a private
		// address is reachable from this host and nowhere else, so without
		// them the stack answers nowhere at all. That is what a host with no
		// attachable network gets, and immich came up healthy on 10.100.0.x
		// with no way to open it.
		if err == nil {
			composeYAML, err = composepkg.RepublishPorts(composeYAML, privateOnlyServices(atts, privateNetworkName(id)))
		}
	}
	if err != nil {
		http.Error(w, "network: "+err.Error(), 400)
		return
	}
	// Folder lists: a variable with several folders (host and/or remote)
	// becomes sub-folders of its mount point, named uniquely in ONE pass so a
	// host "/mnt/home/alice" and a remote ".../sea/alice" can't both claim
	// /movies/alice. A lone remote folder takes the mount point itself.
	// Remote volumes are created on the engine this install runs on, not
	// the default one -- a podman install on a host whose default is appjail
	// must still get its NFS volume from podman.
	volBackend := s.primaryBackend()
	if be, ok := s.backend(req.Engine); ok {
		volBackend = be
	}
	if len(remotes) > 0 && !volBackend.Capabilities().RemoteVolumes {
		http.Error(w, "remote folders (nfs://, smb://) need an engine that supports them", 400)
		return
	}
	vctx, vcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer vcancel()
	for name := range pathVars {
		var locals []string
		for _, e := range pathExpansions {
			if e.Var == name {
				locals = e.Hosts
			}
		}
		if locals == nil && req.Values[name] != "" && len(remotes[name]) > 0 {
			locals = []string{req.Values[name]}
		}
		rs := remotes[name]
		if len(locals)+len(rs) < 2 && len(rs) == 0 {
			continue // single host folder: the plain ${VAR} mount stands
		}
		dest := composepkg.MountTargetOf(m.Compose(), name)
		if dest == "" {
			log.Printf("install %s: %s has extra folders but no volume mount in the compose; skipped", id, name)
			continue
		}
		nested := len(locals)+len(rs) > 1
		// Candidate names per folder: basename, then parent-basename, then a
		// numeric suffix -- uniqueSubfolders picks the first free one.
		var cands [][]string
		for _, h := range locals {
			cands = append(cands, subfolderCandidates(h))
		}
		for _, rf := range rs {
			cands = append(cands, subfolderCandidates(rf.Server+"/"+strings.Trim(rf.Path, "/")))
		}
		names := uniqueSubfolders(cands)
		if nested && len(locals) > 0 {
			mounts := make([]composepkg.FolderMount, len(locals))
			for i, h := range locals {
				mounts[i] = composepkg.FolderMount{Host: h, Sub: names[i]}
			}
			composeYAML, err = composepkg.ExpandFolderMountsNamed(composeYAML, name, mounts, false)
			if err != nil {
				http.Error(w, "folder mounts: "+err.Error(), 500)
				return
			}
		}
		for i, rf := range rs {
			vol, err := ensureRemoteVolume(vctx, volBackend, rf)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			at := dest
			if nested {
				at = strings.TrimRight(dest, "/") + "/" + names[len(locals)+i]
			}
			composeYAML, err = composepkg.AttachVolume(composeYAML, composepkg.FirstService, vol, at, false)
			if err != nil {
				http.Error(w, "attach "+vol+": "+err.Error(), 500)
				return
			}
		}
	}

	// Persist the catalog's web-endpoint hint in the saved compose (compose on
	// disk is the source of truth; podman-compose ignores x-* keys). Without it
	// host-net stacks have no discoverable web link -- nothing is published.
	if m.WebPort != "" {
		composeYAML += fmt.Sprintf("\nx-fjord:\n  web_port: %q\n", m.WebPort)
		if m.WebHTTPS {
			composeYAML += "  web_https: true\n"
		}
	}
	// So the stack's parts can find each other: each service's consumers get
	// its NAME, which container DNS resolves on any network they share.
	for k, v := range serviceHostnames(m.Hostnames, plannedAtts, svcModes) {
		res.Env[k] = v
	}
	st := &stack.Stack{Name: id, Compose: composeYAML, Env: buildEnv(res.Env)}
	if err := s.manager.Save(st); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Bind the stack to its engine (chosen or default), honored only if it's
	// actually available; falls back to the default otherwise.
	eng := req.Engine
	if _, ok := s.backend(eng); !ok {
		eng = s.defaultEngine()
	}
	// On appjail with a director bundle, materialize it into the stack dir:
	// appjail-director.yml + Makejail + template.conf + a director-filled .env
	// (overwriting the compose .env Save just wrote). The engine drives
	// appjail-director for any stack whose dir has an appjail-director.yml, so
	// this one write is what routes the stack onto the director path.
	if eng == "appjail" {
		b := m.Appjail()
		if b == nil {
			// An appjail stack IS a director project; without the bundle there
			// is nothing to run. Say so instead of leaving a stack that can't
			// start (the stack dir is removed again below).
			_ = s.manager.Delete(id)
			http.Error(w, "this app's catalog entry has no AppJail bundle (the catalog was built without dbuild, or the app opts out with appjail: false); install it on podman, or refresh the catalog", 400)
			return
		}
		if req.Network == composepkg.Host {
			_ = s.manager.Delete(id)
			http.Error(w, "an appjail stack cannot be installed on host: that is a jail parameter "+
				"rather than a director option, and fjord does not set it yet -- install it on none, "+
				"on a network, or on podman", 400)
			return
		}
		env, err := writeAppjailBundle(st.Dir, id, b, res.Env, composeYAML, req.attachments(), svcModes, req.Network == composepkg.None)
		if err != nil {
			http.Error(w, "appjail bundle: "+err.Error(), 500)
			return
		}
		st.Env = env
	}
	// One state.json write with everything install knows, instead of four
	// load-modify-save round-trips leaving intermediate files behind.
	now := time.Now().UTC().Format(time.RFC3339)
	st.State = &stack.State{
		Origin:       stack.OriginInfo{Type: "catalog", AppID: req.AppID},
		DesiredState: "running",
		Engine:       eng,
		DisplayName:  strings.TrimSpace(req.Name),
		InstalledAt:  now,
		UpdatedAt:    now,
	}
	copyStackIcon(st.Dir, s.cat.AppIconPath(req.AppID)) // keep the icon with the stack
	if err := s.manager.SaveState(id, st.State); err != nil {
		log.Printf("install %s: persist state: %v", id, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	// The client sent a display name but the stack's id was allocated here --
	// hand it back on EVERY response from here on (the stack exists on disk
	// now, even when the bring-up is refused), so the UI can navigate to it
	// instead of keeping a phantom "installing:<name>" draft.
	w.Header().Set("X-Fjord-Stack-Id", id)
	// The stack is saved either way (so the user can edit ports and start it),
	// but abort the bring-up with a clear message if it can't bind its ports.
	if problems := s.preflight(ctx, st); len(problems) > 0 {
		http.Error(w, "pre-flight failed (stack saved, not started):\n  "+strings.Join(problems, "\n  "), 409)
		return
	}
	stream, err := s.backendFor(st).Up(ctx, st)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	streamOutput(w, stream)
}

// pathExpansion is a host-path variable whose folder list has more than one
// entry, to be expanded into N binds after the compose is rendered.
type pathExpansion struct {
	Var   string
	Hosts []string
}

// applyPathLists folds the wizard's per-variable folder lists into values (the
// first folder is the variable's value) and returns the variables that need
// multi-bind expansion, in a deterministic order.
func applyPathLists(values map[string]string, paths map[string][]string) []pathExpansion {
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []pathExpansion
	for _, name := range names {
		var hosts []string
		for _, h := range paths[name] {
			if h = strings.TrimSpace(h); h != "" {
				hosts = append(hosts, h)
			}
		}
		if len(hosts) == 0 {
			continue
		}
		values[name] = hosts[0]
		if len(hosts) > 1 {
			out = append(out, pathExpansion{Var: name, Hosts: hosts})
		}
	}
	return out
}

// pathTemplateRe matches the placeholders allowed in host paths. {{base}} is
// the older spelling of {{appdata}}; both stay accepted.
var pathTemplateRe = regexp.MustCompile(`\{\{\s*(stack|appdata|base)\s*\}\}`)

// expandPathTemplate substitutes {{stack}} (the app's folder name / storage
// slug) and {{appdata}} (the App data folder) in one host path.
func expandPathTemplate(p, slug, base string) string {
	return pathTemplateRe.ReplaceAllStringFunc(p, func(m string) string {
		if strings.Contains(m, "stack") {
			return slug
		}
		return base
	})
}

// expandPathTemplates resolves placeholders in every path variable's value and
// multi-folder expansion in place, and returns the resolved paths that live
// under base (i.e. belong to this stack) as dirs to provision. Paths elsewhere
// on the host are the user's and are left alone.
func expandPathTemplates(values map[string]string, exps []pathExpansion, pathVars map[string]bool, slug, base string) []manifest.ProvisionDir {
	var dirs []manifest.ProvisionDir
	seen := map[string]bool{}
	own := func(p string) {
		if strings.HasPrefix(p, strings.TrimRight(base, "/")+"/") && !seen[p] {
			seen[p] = true
			dirs = append(dirs, manifest.ProvisionDir{Path: p, Uid: 1000, Gid: 1000, Mode: 0o755})
		}
	}
	for name := range pathVars {
		if v, ok := values[name]; ok && v != "" {
			values[name] = expandPathTemplate(v, slug, base)
			own(values[name])
		}
	}
	for i := range exps {
		for j, h := range exps[i].Hosts {
			exps[i].Hosts[j] = expandPathTemplate(h, slug, base)
			own(exps[i].Hosts[j])
		}
	}
	return dirs
}

// subfolderCandidates lists sub-folder names for a path in preference order:
// its basename, then "<parent>-<basename>" (both path-safe).
func subfolderCandidates(p string) []string {
	p = strings.Trim(p, "/")
	parts := strings.Split(p, "/")
	base := composepkg.SubfolderName(parts[len(parts)-1])
	if base == "" {
		base = "folder"
	}
	out := []string{base}
	if len(parts) > 1 {
		if parent := composepkg.SubfolderName(parts[len(parts)-2]); parent != "" {
			out = append(out, parent+"-"+base)
		}
	}
	return out
}

// uniqueSubfolders assigns each folder the first candidate no other folder
// has taken, falling back to a numeric suffix, so N folders never map to the
// same mount destination.
func uniqueSubfolders(cands [][]string) []string {
	used := map[string]bool{}
	out := make([]string, len(cands))
	for i, cs := range cands {
		chosen := ""
		for _, c := range cs {
			if !used[c] {
				chosen = c
				break
			}
		}
		if chosen == "" {
			base := cs[0]
			for n := 2; ; n++ {
				if c := fmt.Sprintf("%s-%d", base, n); !used[c] {
					chosen = c
					break
				}
			}
		}
		used[chosen] = true
		out[i] = chosen
	}
	return out
}
