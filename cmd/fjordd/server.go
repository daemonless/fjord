package main

import (
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daemonless/fjord/pkg/catalog"
	"github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// server bundles the daemon's dependencies so handlers are methods instead of
// closures over main(). Handlers stay runtime-agnostic: everything they know
// about the container runtime comes through engine.Backend.
type server struct {
	manager *stack.Manager
	// The engine registry is swapped whole by rebuildBackends (an Extensions
	// toggle) while other requests read it; mu makes that a clean handoff.
	mu         sync.RWMutex
	backends   map[string]engine.Backend // available runtimes, keyed by engine name
	defEngine  string                    // default engine for new installs
	cat        *catalog.Cache
	fjordRoot  string       // parent of the stacks dir; provisioned volumes live here
	stacksDir  string       // reported by /api/about
	listenAddr string       // reported by /api/about
	fleet      fleetUpdates // cached fleet-wide update state
	events     *eventHub    // SSE pub/sub; fed by runEventLoop

	catalogAutoMu  sync.Mutex // last automatic catalog refresh (catalog_scheduler.go)
	catalogAutoAt  time.Time
	catalogAutoErr string
}

// backendFor returns the runtime a stack runs on. A stack is bound to one
// engine for life; if that engine isn't registered (appjail vanished, fjordd
// moved into a container) the result is engine.Unavailable for THAT engine,
// never a fallback to the default -- bringing an appjail stack up with
// podman-compose would create a second copy of it, and Down/Delete would
// "succeed" without touching the jails. Never nil (see primaryBackend).
func (s *server) backendFor(st *stack.Stack) engine.Backend {
	name := st.EngineName()
	if b, ok := s.backend(name); ok {
		return b
	}
	return engine.Unavailable(name)
}

// backend looks up a registered engine by name.
func (s *server) backend(name string) (engine.Backend, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.backends[name]
	return b, ok
}

// defaultEngine is the engine new installs use unless the wizard picks another.
func (s *server) defaultEngine() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.defEngine
}

// remoteVolumeBackend returns an engine that can mount remote (nfs/smb) folders
// and store SMB credentials -- the default engine when it can, else any that
// can, or nil if none do.
func (s *server) remoteVolumeBackend() engine.Backend {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if be, ok := s.backends[s.defEngine]; ok && be.Capabilities().RemoteVolumes {
		return be
	}
	for _, be := range s.backends {
		if be.Capabilities().RemoteVolumes {
			return be
		}
	}
	return nil
}

// engineCount is how many engines are registered.
func (s *server) engineCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.backends)
}

// setDefaultEngine records the operator's default; the caller has verified
// it's registered.
func (s *server) setDefaultEngine(name string) {
	s.mu.Lock()
	s.defEngine = name
	s.mu.Unlock()
}

// primaryBackend is the default engine's backend -- used for engine-agnostic
// queries (volumes, networks, host port scan) that aren't tied to a stack.
// Never nil (see backendFor).
func (s *server) primaryBackend() engine.Backend {
	if b, ok := s.backend(s.defaultEngine()); ok {
		return b
	}
	return engine.Unavailable("")
}

// rebuildBackends re-derives the engine registry + default from current settings
// (e.g. after enabling/disabling an engine in the Plugins tab), so a toggle takes
// effect without restarting fjordd.
func (s *server) rebuildBackends() {
	backends, def := buildBackends(s.fjordRoot)
	s.mu.Lock()
	s.backends, s.defEngine = backends, def
	s.mu.Unlock()
}

// backendForRequest routes an engine-agnostic query to the right runtime: if
// the request names a stack (?stack=<id>), the resources shown must come from
// THAT stack's engine (an appjail stack sees appjail networks, not podman's).
// With no stack it's the default engine's view (Volumes page, install wizard).
func (s *server) backendForRequest(r *http.Request) engine.Backend {
	if id := r.URL.Query().Get("stack"); id != "" {
		if st, err := s.manager.Get(id); err == nil {
			return s.backendFor(st)
		}
	}
	if name := r.URL.Query().Get("engine"); name != "" {
		if b, ok := s.backend(name); ok {
			return b
		}
	}
	return s.primaryBackend()
}

// namedEngineMissing reports a request that asked for an engine this daemon
// does not have -- disabled in the Plugins tab, or never installed.
//
// backendForRequest falls back to the primary backend, which is right for a
// request that named no engine and wrong for one that did: asking for podman
// and being handed appjail's answer under podman's name is not a fallback, it
// is a wrong answer delivered confidently. Callers that accept ?engine= check
// this first and say so instead.
func (s *server) namedEngineMissing(r *http.Request) string {
	name := r.URL.Query().Get("engine")
	if name == "" || r.URL.Query().Get("stack") != "" {
		return ""
	}
	if _, ok := s.backend(name); ok {
		return ""
	}
	return "the " + name + " engine is not available on this host -- it is not installed, or it is turned off in Settings > Plugins"
}

// routes registers every HTTP handler on mux.
func (s *server) routes(mux *http.ServeMux) {
	mux.HandleFunc("/catalog/", s.handleCatalogFiles)
	mux.HandleFunc("/api/catalog/refresh", s.handleCatalogRefresh)
	mux.HandleFunc("/api/registry/versions", s.handleRegistryVersions)
	mux.HandleFunc("/api/registry/rolling", s.handleRegistryRolling)
	mux.HandleFunc("/api/compose/mounts", s.handleComposeMounts)
	mux.HandleFunc("/api/settings/storage", s.handleStorageSettings)
	mux.HandleFunc("/api/settings/wizard", s.handleWizardSettings)
	mux.HandleFunc("/api/settings/network", s.handleDefaultNetwork)
	mux.HandleFunc("/api/settings/catalog-refresh", s.handleCatalogRefreshSettings)
	mux.HandleFunc("/api/setup/state", s.handleSetupState)
	mux.HandleFunc("/api/folder-sets", s.handleFolderSets)
	mux.HandleFunc("/api/plugins", s.handlePlugins)
	mux.HandleFunc("/api/maintenance/df", s.handleDiskUsage)
	mux.HandleFunc("/api/maintenance/prune", s.handlePrune)
	mux.HandleFunc("/api/networks", s.handleNetworks)
	mux.HandleFunc("/api/networks/suggest", s.handleNetworkSuggest)
	mux.HandleFunc("/api/networks/kinds", s.handleNetworkKinds)
	mux.HandleFunc("/api/networks/parents", s.handleNetworkParents)
	mux.HandleFunc("/api/networks/setup", s.handleNetworkSetup)
	mux.HandleFunc("/api/networks/", s.handleNetworkDelete)
	mux.HandleFunc("/api/volumes", s.handleVolumes)
	mux.HandleFunc("/api/volumes/ensure", s.handleVolumeEnsure)
	mux.HandleFunc("/api/volumes/smb-credentials", s.handleSMBCredentials)
	mux.HandleFunc("/api/volumes/", s.handleVolumeDelete)
	mux.HandleFunc("/api/apps/install", s.handleInstall)
	mux.HandleFunc("/api/adopt", s.handleAdopt)
	mux.HandleFunc("/api/stacks", s.handleStacksList)
	mux.HandleFunc("/api/stacks/reorder", s.handleReorder) // exact match beats the subtree below
	mux.HandleFunc("/api/stacks/", s.handleStackRoutes)
	mux.HandleFunc("/api/updates", s.handleUpdates)
	mux.HandleFunc("/api/engine", s.handleEngine)
	mux.HandleFunc("/api/engine/install", s.handleEngineInstall)
	mux.HandleFunc("/api/engine/toggle", s.handleEngineToggle)
	mux.HandleFunc("/api/setup", s.handleSetup)
	mux.HandleFunc("/api/about", s.handleAbout)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/browse", s.handleBrowse)
	mux.HandleFunc("/api/catalogs", s.handleCatalogs)
	mux.HandleFunc("/api/catalogs/", s.handleCatalogRoutes)
}

// stackWithStatus is the stacks wire format: the on-disk stack plus its live
// container status from the runtime backend.
type stackWithStatus struct {
	*stack.Stack
	Status engine.StackStatus `json:"status"`
	// Network/NetworkIP are read back out of the compose so the Resources tab
	// can show what the stack is actually attached to. Without them its picker
	// defaults to "bridge", which is wrong for every stack on a network.
	Network    string `json:"network,omitempty"`
	NetworkIP  string `json:"networkIp,omitempty"`
	NetworkMAC string `json:"networkMac,omitempty"`
	// Networks is every network the stack is on, in interface order. The
	// three fields above are the first of them, kept for older clients.
	Networks []compose.Attachment `json:"networks,omitempty"`
	// OwnAddress is true when the first network puts the container on a real
	// segment, so the address it holds is somewhere a browser can go. False
	// for a NAT bridge, where the address is private to the host and the
	// stack's published ports are the way in.
	OwnAddress bool `json:"ownAddress,omitempty"`
	// NetworkMode is the built-in choice the stack sits on -- "none" or
	// "host" -- and empty when it is on a named network or the bridge.
	NetworkMode string `json:"networkMode,omitempty"`
	// NoNamedNetworks is set when this stack cannot move onto a named network
	// at all, and NoModes when it cannot take bridge/host/none. Opposite ends
	// of the same picker, and a stack can be at either -- so the UI grays the
	// half that is out of reach instead of letting Save come back 400.
	NoNamedNetworks bool `json:"noNamedNetworks,omitempty"`
	// UnsupportedModes names the built-in choices this stack cannot take, so
	// the picker can leave them out rather than letting Save come back 400.
	// Not all-or-nothing: an appjail director stack cannot take host or none
	// (both are jail parameters, not director options) but bridge is simply
	// appjail's own NAT virtualnet, which is exactly what bridge means.
	UnsupportedModes []string `json:"unsupportedModes,omitempty"`
	// StashedNetworks is what the stack was attached to before it was put on
	// a mode. Putting it on one does not throw the addresses away, the same
	// way it does not throw the published ports away.
	StashedNetworks []compose.Attachment `json:"stashedNetworks,omitempty"`
}

// buildEnv renders resolved variables into .env lines, sorted for determinism.
// Empty values are skipped -- their compose references were already dropped.
func buildEnv(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if env[k] == "" {
			continue
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(env[k])
		b.WriteByte('\n')
	}
	return b.String()
}

// streamOutput pipes a command stream to the HTTP response verbatim (ANSI
// escapes + \r intact) -- the UI renders it in xterm.js, a real terminal
// emulator, so colors and in-place progress lines display correctly.
func streamOutput(w http.ResponseWriter, stream io.ReadCloser) {
	// The backends hand back the read end of an io.Pipe fed by a goroutine.
	// Closing it unblocks that writer if the client vanished mid-stream --
	// without it a follow (logs -f) leaves the goroutine and its podman
	// children parked on a write nobody will ever drain.
	defer stream.Close()
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	buf := make([]byte, 1024)
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			break
		}
	}
}
