package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/registry"
	"github.com/daemonless/fjord/pkg/sbom"
	"github.com/daemonless/fjord/pkg/stack"
	"github.com/daemonless/fjord/pkg/updates"
	"gopkg.in/yaml.v3"
)

// saveRequest is the /api/stacks/<n>/save payload: the editable stack fields
// plus an optional macvlan attachment applied to the compose before writing.
// Network/IP are only sent when a stack is first created/attached, not on
// every edit.
// attachments mirrors installRequest.attachments: the list when given, else
// the single Network/IP/MAC triple.
func (r saveRequest) attachments() []composepkg.Attachment {
	if len(r.Networks) > 0 {
		return r.Networks
	}
	if r.Network == "" || composepkg.BuiltIn(r.Network) {
		return nil
	}
	return []composepkg.Attachment{{Network: r.Network, IP: r.IP, MAC: r.MAC}}
}

type saveRequest struct {
	Compose string `json:"compose"`
	Env     string `json:"env"`
	// BaseHash is the composeHash the editor loaded. A save whose base no
	// longer matches the file is refused -- see stackSave.
	BaseHash string `json:"baseHash,omitempty"`
	// Director/Makejail are the appjail-director.yml and Makejail: accepted on
	// create when Engine is appjail (a native appjail stack, no compose), and on
	// edit only for a stack that already runs via director (a compose stack
	// can't sprout one).
	Director string `json:"director,omitempty"`
	Makejail string `json:"makejail,omitempty"`
	// Engine binds a NEW stack to a runtime (podman|appjail); ignored on edit.
	Engine string `json:"engine,omitempty"`
	// DisplayName labels a NEW stack (renaming an existing one is /rename).
	DisplayName string `json:"displayName,omitempty"`
	Network     string `json:"network,omitempty"` // attach the stack to this macvlan network
	IP          string `json:"ip,omitempty"`      // optional predictable IP within it
	MAC         string `json:"mac,omitempty"`     // optional pinned MAC, for a DHCP reservation
	// Networks is the full list when a stack takes more than one interface;
	// Network/IP/MAC above remain the single-network form.
	Networks []composepkg.Attachment `json:"networks,omitempty"`
	// NetworkModes is service -> built-in (host/bridge/none). A mode is set ON
	// the service rather than joined, so it cannot travel as an attachment --
	// and without it there was no way to take one service off the host's stack
	// while leaving the others, which is what the per-service editor is for.
	NetworkModes map[string]string `json:"networkModes,omitempty"`
	// One-shot volume attachment: mount the named volume at VolumePath.
	Volume     string `json:"volume,omitempty"`
	VolumePath string `json:"volumePath,omitempty"`
	VolumeRO   bool   `json:"volumeRO,omitempty"`
}

// handleStacksList lists stacks with their compose/.env and live container
// status. Carrying the (small) compose text here lets the dashboard derive
// tags and links for every tile from one request instead of one per stack.
func (s *server) handleStacksList(w http.ResponseWriter, r *http.Request) {
	stacks, err := s.manager.List()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	enriched := make([]stackWithStatus, 0, len(stacks))
	for _, st := range stacks {
		if full, err := s.manager.Get(st.Name); err == nil {
			st = full
		}
		status, err := s.backendFor(st).Status(r.Context(), st)
		if err != nil {
			status = engine.StackStatus{State: "unknown"}
		}
		// The list needs the networks too: without them its Open link cannot
		// tell "publishes on the host" from "has its own address", and builds
		// a host URL that times out.
		atts := composepkg.AttachedNetworks(st.Compose)
		if st.Director != "" {
			atts = directorAttachments(st.Director)
		} else {
			nameIfaces(atts)
		}
		svcs := stackServices(st, status)
		row := stackWithStatus{Stack: st, Status: status, Networks: atts,
			NetworkMode: composepkg.NetworkMode(st.Compose),
			Services:    svcs,
			LinkHost:    linkHost(svcs, ownAddress)}
		// The legacy single-network fields. With per-service networking they
		// are an arbitrary pick among several, so nothing new should read them
		// -- Services carries the truth, and LinkHost the one answer the UI
		// needs. Kept because older clients still ask for them.
		if len(atts) > 0 {
			row.Network, row.NetworkIP, row.NetworkMAC = atts[0].Network, atts[0].IP, atts[0].MAC
			row.OwnAddress = ownAddress(atts[0].Network)
		}
		enriched = append(enriched, row)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(enriched)
}

// ownAddress reports whether a network gives a container an address of its
// own on a real segment, rather than one behind the host's NAT.
//
// The network's own definition says which: an epair puts the container on the
// bridge's segment, any other driver is a bridge the runtime NATs. Guessing
// from "is it attached" gets a private network wrong -- it is attached, and
// its 10.x address is no more reachable from a browser than the default
// bridge's is.
func ownAddress(network string) bool {
	def, ok := hostnet.Get(network)
	return ok && def.Type == "epair"
}

// nameIfaces fills in what a container will call each interface. podman numbers
// them from eth0 in attachment order; appjail names them after the option that
// made them, which directorAttachments reports instead.
func nameIfaces(atts []composepkg.Attachment) {
	for i := range atts {
		atts[i].Iface = fmt.Sprintf("eth%d", i)
	}
}

// unsupportedModes lists the built-in network choices a stack cannot take.
//
// A director stack's networking is the director's -- appjail never reads its
// compose for it -- and fjord has no way to PUT a director project on host:
// that is a jail parameter rather than a director option.
//
// But a bundle can arrive already on it. dbuild writes `ip4_inherit` into the
// director options and `ip4: inherit` into the jail template for a
// host-networked app, which is exactly how immich's four services find each
// other on 127.0.0.1. Calling host unsupported there told the operator their
// stack was in a state it could not be in, and left the picker unable to show
// the stack's own current mode -- every option greyed and the select holding a
// value that was not among them.
//
// bridge is not in the list: for appjail that is its own NAT virtualnet, which
// is what bridge means on every engine.
func unsupportedModes(st *stack.Stack) []string {
	if st.Director == "" {
		return nil
	}
	if directorInheritsHost(st.Director) {
		return nil
	}
	// none is NOT in the list: a director project takes it by having no
	// network option at all, which is exactly what it means.
	return []string{composepkg.Host}
}

// directorInheritsHost reports a director project whose options put its jails
// on the host's stack (`ip4_inherit`, and `ip6_inherit` for the v6 half).
func directorInheritsHost(directorYML string) bool {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(directorYML), &doc) != nil || len(doc.Content) == 0 {
		return false
	}
	opts := mapKey(doc.Content[0], "options")
	if opts == nil || opts.Kind != yaml.SequenceNode {
		return false
	}
	for _, item := range opts.Content {
		if item.Kind == yaml.MappingNode && len(item.Content) >= 1 {
			switch item.Content[0].Value {
			case "ip4_inherit", "ip6_inherit":
				return true
			}
		}
	}
	return false
}

// handleStackRoutes dispatches /api/stacks/<name>[/<action>].
func (s *server) handleStackRoutes(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/stacks/"), "/")
	// The name becomes a host path component; the mux only canonicalises
	// literal dots, so an escaped %2E%2E still arrives here as "..".
	if !stack.ValidName(parts[0]) {
		http.Error(w, "invalid stack name: use letters, digits, '_' or '-'", 400)
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			s.stackDetail(w, r, parts[0])
			return
		case http.MethodDelete:
			s.stackDelete(w, parts[0])
			return
		}
	}
	if r.Method == http.MethodGet && len(parts) == 2 {
		switch parts[1] {
		case "update-check":
			s.stackUpdateCheck(w, parts[0])
			return
		case "changes":
			s.stackChanges(w, r, parts[0])
			return
		case "icon":
			s.stackIcon(w, r, parts[0])
			return
		case "exec":
			s.stackExec(w, r, parts[0])
			return
		case "logs":
			s.stackLogs(w, r, parts[0])
			return
		}
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	name, action := parts[0], parts[1]
	switch action {
	case "save":
		s.stackSave(w, r, name)
	case "set-tag":
		s.stackSetTag(w, r, name)
	case "group":
		s.stackGroup(w, r, name)
	case "rename":
		s.stackRename(w, r, name)
	case "policy":
		s.stackPolicy(w, r, name)
	case "rollback":
		s.stackRollback(w, r, name)
	case "unpin":
		s.stackUnpin(w, r, name)
	default:
		s.stackLifecycle(w, r, name, action)
	}
}

// stackDetail returns the full stack (compose, env, state) plus live container
// status. The list endpoint omits compose content, so the editor fetches it
// here; status is included so a deep-link/refresh doesn't render "unknown".
func (s *server) stackDetail(w http.ResponseWriter, r *http.Request, name string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	status, err := s.backendFor(st).Status(r.Context(), st)
	if err != nil {
		status = engine.StackStatus{State: "unknown"}
	}
	w.Header().Set("Content-Type", "application/json")
	// appjail never reads compose.yaml for networking, so for a director stack
	// the compose's networks: block is a wish and the director is the fact.
	// Reporting the wish showed four interfaces for a jail that had one.
	atts := composepkg.AttachedNetworks(st.Compose)
	if st.Director != "" {
		atts = directorAttachments(st.Director)
	} else {
		nameIfaces(atts)
	}
	svcs := stackServices(st, status)
	net, ip, mac := "", "", ""
	own := false
	if len(atts) > 0 {
		net, ip, mac = atts[0].Network, atts[0].IP, atts[0].MAC
		own = ownAddress(net)
	}
	json.NewEncoder(w).Encode(stackWithStatus{
		Stack: st, Status: status, ComposeHash: composeHash(st), Network: net, NetworkIP: ip, NetworkMAC: mac,
		Networks: atts, OwnAddress: own, Services: svcs, LinkHost: linkHost(svcs, ownAddress),
		// A stack on a mode has no attachments, which on its own is
		// indistinguishable from one on the bridge publishing ports.
		NetworkMode:      composepkg.NetworkMode(st.Compose),
		NoNamedNetworks:  composepkg.NoNamedNetworks(st.Compose),
		UnsupportedModes: unsupportedModes(st),
		// What it was on before the mode, so the picker can offer it back
		// rather than making the user retype addresses that are still here.
		StashedNetworks: composepkg.StashedNetworks(st.Compose),
	})
}

// stackDelete stops the stack's containers, then removes its stack dir.
// Bind-mounted container data is untouched. If containers survive the
// teardown (Down streams its errors rather than returning them), the delete
// is refused: removing the dir would orphan running containers that fjord
// could no longer see or stop.
func (s *server) stackDelete(w http.ResponseWriter, name string) {
	unlock, ok := lockStack(name)
	if !ok {
		http.Error(w, "another operation is already running on this stack; wait for it to finish", http.StatusConflict)
		return
	}
	defer unlock()
	if st, err := s.manager.Get(name); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		be := s.backendFor(st)
		if stream, err := be.Down(ctx, st); err == nil {
			io.Copy(io.Discard, stream) // block until teardown finishes
			stream.Close()
		}
		if status, err := be.Status(ctx, st); err == nil {
			var alive []string
			for _, c := range status.Containers {
				if c.State != "stopped" && c.State != "" {
					alive = append(alive, c.Name+" ("+c.State+")")
				}
			}
			if len(alive) > 0 {
				cancel()
				http.Error(w, "not deleted: containers are still up after the stop attempt -- "+strings.Join(alive, ", ")+". Check Output/Logs, stop them, then delete again.", http.StatusConflict)
				return
			}
		}
		cancel()
	}
	if err := s.manager.Delete(name); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Stack not found", 404)
		} else {
			http.Error(w, err.Error(), 500)
		}
		return
	}
	log.Printf("delete %s", name)
	s.fleet.forget(name) // no lingering "update available" for a gone stack
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"deleted"}`))
}

// stackRename sets a stack's display name -- an instant metadata edit. The
// stack's identity (dir, compose project, containers, volumes) is the id and
// never changes, so nothing is stopped or recreated.
func (s *server) stackRename(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		NewName string `json:"newName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", 400)
		return
	}
	if !stack.ValidDisplayName(req.NewName) {
		http.Error(w, "invalid name: letters, digits, spaces, '.', '_' and '-' only (max 64)", 400)
		return
	}
	if _, err := s.manager.Get(id); err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	if err := s.manager.SetDisplayName(id, req.NewName); err != nil {
		http.Error(w, "rename: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "renamed", "displayName": req.NewName})
}

// stackUpdateCheck reports whether the stack's images are behind the registry.
func (s *server) stackUpdateCheck(w http.ResponseWriter, name string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status := updates.Check(ctx, s.backendFor(st), s.updateServices(ctx, st), s.schemeFor)
	s.markCandidates(status)
	w.Header().Set("Content-Type", "application/json")
	// perService: whether Update can take just the services that changed, so
	// the panel offers that rather than a whole-stack recreate.
	// restartsWith: what depends on each service, so the panel can say an
	// update of the database restarts the server too (it has to; see
	// composepkg.WithDependents).
	restarts := map[string][]string{}
	for _, sv := range status.Services {
		if more := composepkg.WithDependents(st.Compose, []string{sv.Service}); len(more) > 1 {
			restarts[sv.Service] = slices.DeleteFunc(more, func(n string) bool { return n == sv.Service })
		}
	}
	// rollback: services whose last update can be undone -- a recorded
	// earlier image that is not what the service runs now.
	type rollbackTo struct {
		Ref string `json:"ref"`
		At  string `json:"at"`
	}
	rollback := map[string]rollbackTo{}
	if state, _ := s.manager.LoadState(name); state != nil && s.backendFor(st).Capabilities().UpdateServices {
		for _, sv := range status.Services {
			if rb, ok := state.Rollback[sv.Service]; ok && sv.Running != "" && sv.Running != rb.Digest {
				rollback[sv.Service] = rollbackTo{rb.Ref, rb.At}
			}
		}
	}
	json.NewEncoder(w).Encode(struct {
		updates.Status
		PerService   bool                  `json:"perService"`
		RestartsWith map[string][]string   `json:"restartsWith,omitempty"`
		Rollback     map[string]rollbackTo `json:"rollback,omitempty"`
	}{status, s.backendFor(st).Capabilities().UpdateServices, restarts, rollback})
}

// stackChanges says what updating one service would change:
// GET .../changes?service=<name>. The running image is compared with what the
// update would install -- the tag's current image, or the newer version's
// when the service is behind by one -- using what each image says about
// itself in the registry. Nothing is pulled.
func (s *server) stackChanges(w http.ResponseWriter, r *http.Request, name string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	want := r.URL.Query().Get("service")
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var sv *updates.Service
	for _, c := range s.updateServices(ctx, st) {
		if c.Name == want {
			sv = &c
			break
		}
	}
	if sv == nil {
		http.Error(w, fmt.Sprintf("%s has no service %q", name, want), 400)
		return
	}
	target := sv.Image
	up := updates.Check(ctx, s.backendFor(st), []updates.Service{*sv}, s.schemeFor)
	if up.State == "upgrade" && up.NewTag != "" {
		target = registry.Repo(sv.Image) + ":" + up.NewTag
	}
	newIndex, err := registry.Digest(ctx, target)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	newDoc, err := sbom.For(ctx, target, newIndex, platformOf(ctx, target, newIndex))
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	// The running side is best effort: an old image can be gone from the
	// registry, and the answer is then the new side on its own.
	var oldDoc *sbom.Doc
	if sv.Running != "" {
		oldDoc, _ = sbom.For(ctx, sv.Image, sv.Running, platformOf(ctx, sv.Image, sv.Running))
	}
	diff := sbom.Compare(oldDoc, newDoc)
	// The versions the class is judged by: labels, else the SBOM's entry for
	// the app, else the tags a version bump moves between.
	repo := registry.Repo(sv.Image)
	from, to := oldDoc.AppVersion(repo), newDoc.AppVersion(repo)
	if from == "" && to == "" && up.State == "upgrade" {
		from, to = up.FromVersion, up.ToVersion
	}
	if diff.VersionFrom == "" {
		diff.VersionFrom = from
	}
	if diff.VersionTo == "" {
		diff.VersionTo = to
	}
	class := updates.Classify(from, to)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		sbom.Diff
		Class updates.Class `json:"class"`
		// Auto is what auto-update would do with this, under the stack's
		// policy -- shown, not acted on, until the scheduler exists.
		Auto updates.Verdict `json:"auto"`
	}{diff, class, s.verdict(st, sv.Name, class, candidateKey(up.State, sv.Image, up.Latest, up.NewTag))})
}

// platformOf is this host's manifest inside an index, or the digest itself
// when it names a single manifest.
func platformOf(ctx context.Context, image, digest string) string {
	if p, err := registry.PlatformDigest(ctx, image, digest); err == nil && p != "" {
		return p
	}
	return digest
}

// stackLogs streams container logs: GET .../logs?tail=200&follow=1. Uses the
// request context so a client disconnect kills the log process (important for
// follow, which otherwise never exits).
func (s *server) stackLogs(w http.ResponseWriter, r *http.Request, name string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	tail := 200
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, e := strconv.Atoi(t); e == nil {
			tail = n
		}
	}
	stream, err := s.backendFor(st).Logs(r.Context(), st, tail, r.URL.Query().Get("follow") == "1", r.URL.Query()["container"])
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	streamOutput(w, stream)
}

// stackSave persists edited compose/.env, optionally attaching a macvlan
// network so the stack gets its own IP.
func (s *server) stackSave(w http.ResponseWriter, r *http.Request, name string) {
	var payload saveRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON payload", 400)
		return
	}
	// fjord writes the compose itself -- a rollback pins an image, a version
	// change retags one, unpin restores one -- while the editor may still hold
	// the copy it loaded. Saving that copy put the old image straight back:
	// a rollback of zensical was undone by two Saves during its health watch.
	// So a save must say which version it edited, and a stale one is refused.
	if payload.BaseHash != "" {
		if existing, err := s.manager.Get(name); err == nil && composeHash(existing) != payload.BaseHash {
			http.Error(w, name+"'s compose changed on the server since you opened it (a rollback, version change or "+
				"another tab). Reload to see it -- copy your edits first, reloading replaces them.", http.StatusConflict)
			return
		}
	}
	// A top-level `name:` would make podman-compose label containers with it
	// instead of the stack id, hiding them from status/logs/delete -- the
	// same strip the catalog install path applies.
	composeYAML := composepkg.DropTopLevelKey(payload.Compose, "name")
	// The built-ins are states, not networks to attach to: "bridge" is what a
	// stack gets by asking for nothing, "none" is no network at all.
	// A per-service answer is the whole answer and is read BEFORE the
	// stack-wide built-ins. Read the other way, a stack sitting on "host" had
	// HostNetwork() re-apply host to every service and throw the table away --
	// the same trap the install path had, which is why the shape is the same.
	perSvcSave := len(payload.Networks) > 0 || len(payload.NetworkModes) > 0
	if !perSvcSave && payload.Network == composepkg.None {
		disabled, err := composepkg.DisableNetwork(composeYAML)
		if err != nil {
			http.Error(w, "disable network: "+err.Error(), 400)
			return
		}
		composeYAML = disabled
	} else if !perSvcSave && payload.Network == composepkg.Host {
		hosted, err := composepkg.HostNetwork(composeYAML)
		if err != nil {
			http.Error(w, "host network: "+err.Error(), 400)
			return
		}
		composeYAML = hosted
	} else if !perSvcSave && (payload.Network == composepkg.Bridge ||
		(payload.Networks != nil && len(payload.Networks) == 0)) {
		// An explicit empty list, or "bridge": back to publishing on the host.
		detached, err := composepkg.DetachNetworks(composeYAML)
		if err != nil {
			http.Error(w, "network detach: "+err.Error(), 400)
			return
		}
		composeYAML = detached
	} else if atts := payload.attachments(); len(atts) > 0 || len(payload.NetworkModes) > 0 {
		eng := payload.Engine
		if eng == "" {
			if existing, err := s.manager.Get(name); err == nil {
				eng = existing.EngineName()
			}
		}
		// "private" is a spec, not a name: it means this stack's own segment,
		// which may not exist yet. Same resolution the install path does, so
		// picking it here and picking it there mean the same thing.
		var svcNames []string
		for _, svc := range composepkg.ParseServices(composeYAML, envMap(payload.Env)) {
			svcNames = append(svcNames, svc.Name)
		}
		resolved, modes, planErr := planServiceNetworks(r.Context(), nil, atts, svcNames,
			func() (string, error) { return s.ensurePrivateNetwork(r.Context(), eng, name) })
		if planErr != nil {
			http.Error(w, "network: "+planErr.Error(), 400)
			return
		}
		atts = resolved
		for svc, m := range payload.NetworkModes {
			modes[svc] = m
		}
		for _, a := range atts {
			if msg := s.networkUnusable(r.Context(), eng, a.Network); msg != "" {
				http.Error(w, msg, 400)
				return
			}
		}
		if msg := attachmentsUnusable(atts, s.engineNetworks(r.Context(), eng)); msg != "" {
			http.Error(w, msg, 400)
			return
		}
		if len(atts) > 0 {
			injected, err := composepkg.InjectNetworks(composeYAML, atts)
			if err != nil {
				http.Error(w, "network attach: "+err.Error(), 400)
				return
			}
			composeYAML = injected
		}
		// Modes after: InjectNetworks clears the mode of anything it attaches,
		// so setting them first would undo the ones meant to stay.
		if len(modes) > 0 {
			moded, err := composepkg.SetServiceModes(composeYAML, modes)
			if err != nil {
				http.Error(w, "network mode: "+err.Error(), 400)
				return
			}
			composeYAML = moded
		}
		// A service left only on this stack's private segment is reachable
		// from this host and nowhere else, so it keeps its published ports.
		republished, err := composepkg.RepublishPorts(composeYAML, privateOnlyServices(atts, privateNetworkName(name)))
		if err != nil {
			http.Error(w, "network: "+err.Error(), 400)
			return
		}
		composeYAML = republished
		// The parts can no longer find each other at localhost once they hold
		// separate addresses. Install rewrites these; save did not, so moving
		// a stack off the host's stack left DB_HOSTNAME=localhost and the app
		// came up unable to reach its own database.
		payload.Env = rewriteHostnames(payload.Env, s.appHostnames(r.Context(), name), atts, modes)
	}
	// A director stack's networking is the director's, and nothing was
	// regenerating it on save: the Resources tab edited a compose appjail does
	// not read, so four networks on screen stayed one epair in the jail. Only
	// the install path ever called the generator.
	directorYML := ""
	if existing, err := s.manager.Get(name); err == nil && existing.Director != "" {
		directorYML = existing.Director
		if payload.Director != "" {
			directorYML = payload.Director
		}
		switch {
		case payload.Network == composepkg.None:
			// Not a director option -- the absence of one.
			directorYML, err = disableDirectorNetworks(directorYML)
		case payload.Network == composepkg.Host:
			http.Error(w, "an appjail stack cannot be put on host: that is a jail parameter "+
				"rather than a director option, and fjord does not set it yet -- use none, or a network", 400)
			return
		case payload.Network == composepkg.Bridge || (payload.Networks != nil && len(payload.Networks) == 0):
			directorYML, err = clearDirectorNetworks(directorYML)
		case len(payload.attachments()) > 0:
			directorYML, err = setDirectorNetworks(r.Context(), directorYML, name, payload.attachments())
		}
		if err != nil {
			http.Error(w, "network attach: "+err.Error(), 400)
			return
		}
	}

	if payload.Volume != "" {
		attached, err := composepkg.AttachVolume(composeYAML, composepkg.FirstService, payload.Volume, payload.VolumePath, payload.VolumeRO)
		if err != nil {
			http.Error(w, "volume attach: "+err.Error(), 400)
			return
		}
		composeYAML = attached
	}
	// If it's a new stack it might not exist on disk yet; use the payload.
	st := &stack.Stack{Name: name, Compose: composeYAML, Env: payload.Env}
	created := false
	if existing, err := s.manager.Get(name); err == nil {
		existing.Compose = composeYAML
		existing.Env = payload.Env
		if existing.Director != "" {
			if directorYML != "" {
				existing.Director = directorYML
			} else if payload.Director != "" {
				existing.Director = payload.Director
			}
			if payload.Makejail != "" {
				existing.Makejail = payload.Makejail
			}
		}
		st = existing
	} else {
		created = true
		// A new native appjail stack is director + Makejail, no compose.
		if payload.Engine == "appjail" && payload.Director != "" {
			if _, ok := s.backend("appjail"); !ok {
				http.Error(w, "the appjail engine is not available on this host", 400)
				return
			}
			if need := servicesWithoutMakejail(payload.Director); strings.TrimSpace(payload.Makejail) == "" && len(need) > 0 {
				http.Error(w, strings.Join(need, ", ")+" names no makejail: of its own, so it builds from this stack's "+
					"Makejail -- write one, or give each service a makejail: (e.g. gh+AppJail-makejails/<app>)", 400)
				return
			}
			st.Director, st.Makejail = payload.Director, payload.Makejail
		}
	}
	if err := s.manager.Save(st); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Save has just written the director; the templates it references need
	// their networked variants next to it. Done here rather than only at
	// install so a stack installed before this existed gets them the first
	// time it is saved -- which is the save that puts it on a network.
	if st.Director != "" && st.Dir != "" {
		if err := ensureNetTemplates(st.Dir, st.Director); err != nil {
			http.Error(w, "jail templates: "+err.Error(), 500)
			return
		}
	}
	if created {
		eng := payload.Engine
		if eng == "" {
			eng = s.defaultEngine()
		}
		log.Printf("create %s (%s, director=%v)", name, eng, st.Director != "")
	} else {
		log.Printf("save %s", name)
	}
	// Record installed/updated timestamps; leaves desired_state alone.
	if err := s.manager.EnsureState(name); err != nil {
		log.Printf("save %s: persist state: %v", name, err)
	}
	// Bind a new stack to its chosen engine (only if that engine is registered;
	// unset means podman). Existing stacks keep theirs -- switching runtimes is
	// a recreate, not an edit.
	if created && payload.Engine != "" {
		if _, ok := s.backend(payload.Engine); !ok {
			http.Error(w, "unknown engine "+payload.Engine, 400)
			return
		}
		if err := s.manager.SetEngine(name, payload.Engine); err != nil {
			log.Printf("save %s: set engine: %v", name, err)
		}
	}
	if created && strings.TrimSpace(payload.DisplayName) != "" {
		if err := s.manager.SetDisplayName(name, payload.DisplayName); err != nil {
			log.Printf("save %s: set display name: %v", name, err)
		}
	}
	hash := ""
	if saved, err := s.manager.Get(name); err == nil {
		hash = composeHash(saved)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "saved", "composeHash": hash})
}

// composeHash identifies what a stack's runtime spec says: the compose, and
// the director spec when there is one (a rollback can rewrite either).
func composeHash(st *stack.Stack) string {
	sum := sha256.Sum256([]byte(st.Compose + "\x00" + st.Director))
	return hex.EncodeToString(sum[:8])
}

// stackSetTag rewrites the stack's image ref -- change train/version and/or
// pin to an exact digest. pin=true freezes each service image to the digest
// its tag resolves to now ("repo:tag" -> "repo:tag@sha256:..."), so a later
// tag move can't change what's deployed. SetImageTag drops any existing
// @digest first, so pin=false (or omitted) unpins. The UI redeploys afterward
// only when the version actually changed. With service set, only that
// service's image moves -- how a multi-image stack takes a new version.
func (s *server) stackSetTag(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		Tag     string `json:"tag"`
		Pin     bool   `json:"pin"`
		Service string `json:"service"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Tag == "" {
		http.Error(w, "tag required", 400)
		return
	}
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	if body.Service != "" {
		// A director stack's jails read the tag from the director file, which
		// has no per-service form here; retagging the compose alone would
		// show one version and run another.
		if st.Director != "" {
			http.Error(w, "an appjail stack changes version for the whole stack", 400)
			return
		}
		newCompose, err := composepkg.SetServiceTag(st.Compose, body.Service, body.Tag)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		st.Compose = newCompose
		if err := s.manager.Save(st); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
		return
	}
	// SetImageTag rewrites EVERY service image -- correct for single-image
	// stacks, destructive for multi-image ones (it would retag the db/redis
	// to an app version). Those name the service.
	if images, _ := composepkg.ServiceImages(st.Compose); len(images) > 1 {
		http.Error(w, "multi-service stack: name the service to retag", 400)
		return
	}
	newCompose, err := composepkg.SetImageTag(st.Compose, body.Tag)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if body.Pin {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		newCompose, err = composepkg.PinImageDigests(newCompose, func(ref string) (string, error) {
			return registry.Digest(ctx, ref)
		})
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
	}
	st.Compose = newCompose
	// And where appjail will read it. Setting only the compose left the jail
	// on whatever ${tag} defaults to, so the version on screen and the version
	// running were different numbers.
	if st.Director != "" {
		st.Director, err = setDirectorTag(st.Director, body.Tag)
		if err != nil {
			http.Error(w, "director tag: "+err.Error(), 500)
			return
		}
	}
	if err := s.manager.Save(st); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// stackGroup assigns the stack to a sidebar group (empty = ungrouped).
func (s *server) stackGroup(w http.ResponseWriter, r *http.Request, name string) {
	var body struct {
		Group string `json:"group"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	if err := s.manager.SetGroup(name, strings.TrimSpace(body.Group)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// handleReorder persists the sidebar's drag-and-drop layout: the client sends
// every stack in its new top-to-bottom order with its (possibly changed) group,
// and the manager stamps order + group in one pass.
func (s *server) handleReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Items []stack.OrderItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	if err := s.manager.Reorder(body.Items); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// lifecycleLocks serializes the operations that change a stack's running
// state, one lock per stack. Two compose runs in the same directory race on
// container state and podman's storage lock, and the loser fails with a
// cryptic error after the winner has already half-changed things -- which is
// exactly what a double-click on Start, or Delete while an update is still
// pulling, produces. AppJail stacks queue behind directorLock inside the
// engine, but the overlap arrives here, at the HTTP layer, for both engines.
//
// Held with TryLock, not Lock: the caller is a browser waiting on a streamed
// response, so the second click is refused immediately rather than parked
// behind a ten-minute pull.
var lifecycleLocks sync.Map

func lockStack(name string) (unlock func(), ok bool) {
	mu, _ := lifecycleLocks.LoadOrStore(name, &sync.Mutex{})
	m := mu.(*sync.Mutex)
	if !m.TryLock() {
		return nil, false
	}
	return m.Unlock, true
}

// requestedServices reads the optional {"services": [...]} body of an update:
// which services to pull and recreate, none meaning all. Each must be one of
// the stack's own -- compose would otherwise answer a typo with "no such
// service" halfway through, after the pull.
func requestedServices(r *http.Request, st *stack.Stack) ([]string, error) {
	var req struct {
		Services []string `json:"services"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			return nil, fmt.Errorf("invalid JSON payload: %v", err)
		}
	}
	if len(req.Services) == 0 {
		return nil, nil
	}
	known, _ := composepkg.ServiceImageList(st.Compose)
	for _, want := range req.Services {
		if !slices.ContainsFunc(known, func(si composepkg.ServiceImage) bool { return si.Service == want }) {
			return nil, fmt.Errorf("%s has no service %q", st.Name, want)
		}
	}
	return req.Services, nil
}

// stackLifecycle runs up/down/update/restart, streaming the backend's output.
func (s *server) stackLifecycle(w http.ResponseWriter, r *http.Request, name, action string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}

	unlock, ok := lockStack(name)
	if !ok {
		http.Error(w, "another operation is already running on this stack; wait for it to finish", http.StatusConflict)
		return
	}
	defer unlock()

	// Generous timeout: update pulls images, which can be slow.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// One line per operator action: without it an incident has to be
	// reconstructed from state.json mtimes and podman events.
	log.Printf("%s %s (%s)", action, name, st.EngineName())

	// Catch doomed bring-ups (taken ports) with a clear message before the
	// compose engine turns them into cryptic failures.
	if action == "up" || action == "update" {
		if problems := s.preflight(ctx, st); len(problems) > 0 {
			http.Error(w, "pre-flight failed:\n  "+strings.Join(problems, "\n  "), 409)
			return
		}
	}

	var stream io.ReadCloser
	switch action {
	case "up":
		stream, err = s.backendFor(st).Up(ctx, st)
	case "down":
		stream, err = s.backendFor(st).Down(ctx, st)
	case "update":
		var services []string
		if services, err = requestedServices(r, st); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if r.Context().Value(isRollbackKey{}) == nil {
			s.recordRollback(ctx, st, services)
		}
		note := keptNote(s.keepAddresses(ctx, st))
		stream, err = s.backendFor(st).Update(ctx, st, services)
		if err == nil && note != "" {
			stream = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(strings.NewReader(note), stream), stream}
		}
	case "restart":
		stream, err = s.backendFor(st).Restart(ctx, st)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// Record operator intent so start-on-boot can restore it after a
	// fjordd/host restart. Best-effort: a state write failure must not abort
	// the lifecycle op the user asked for. (update leaves it running.)
	if desired := map[string]string{"up": "running", "down": "stopped", "update": "running", "restart": "running"}[action]; desired != "" {
		if err := s.manager.SetDesiredState(name, desired); err != nil {
			log.Printf("%s %s: persist desired_state: %v", action, name, err)
		}
	}

	streamOutput(w, stream)

	// The fleet cache holds its verdict for half an hour, and it is what the
	// badge reads. Without this, updating a stack that really was behind left
	// "update available" on screen afterwards -- so the obvious move was to
	// update again, and again, each one working and none of them changing
	// what the page said. Only the two actions that pull: a registry check
	// per stack is rate-limited, and down/restart cannot move an image.
	if action == "up" || action == "update" {
		s.fleet.forget(name)
	}
}
