package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daemonless/fjord/pkg/catalog"
	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/hostnet"
	"github.com/daemonless/fjord/pkg/stack"
)

type planTestBackend struct {
	engine.Backend
	nets []engine.Network
	used map[string]string
	caps engine.Capabilities
}

func (b *planTestBackend) Name() string                      { return "podman" }
func (b *planTestBackend) Capabilities() engine.Capabilities { return b.caps }
func (b *planTestBackend) Networks(context.Context) ([]engine.Network, error) {
	return b.nets, nil
}
func (b *planTestBackend) NetworkParents(context.Context) ([]engine.NetworkParent, error) {
	return nil, nil
}
func (b *planTestBackend) UsedPorts(context.Context) (map[string]string, error) {
	return b.used, nil
}
func (b *planTestBackend) Status(context.Context, *stack.Stack) (engine.StackStatus, error) {
	return engine.StackStatus{State: "running"}, nil
}
func (b *planTestBackend) Up(context.Context, *stack.Stack) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("started\n")), nil
}

func seedTestCatalog(t *testing.T, root, srcID string, apps []map[string]any, manifests map[string]string) *catalog.Cache {
	t.Helper()
	catDir := filepath.Join(root, "catalog")
	srcDir := filepath.Join(catDir, srcID)
	if err := os.MkdirAll(filepath.Join(srcDir, "manifests"), 0o755); err != nil {
		t.Fatal(err)
	}
	rawCat, err := json.Marshal(map[string]any{"apps": apps})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "catalog.json"), rawCat, 0o644); err != nil {
		t.Fatal(err)
	}
	for id, m := range manifests {
		if err := os.WriteFile(filepath.Join(srcDir, "manifests", id+".yaml"), []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return catalog.New(catDir, []catalog.Source{{ID: srcID, Name: srcID, URL: "http://example.invalid/" + srcID}})
}

const radarrPlanManifest = `name: radarr
services:
  radarr:
    image: ghcr.io/daemonless/radarr:latest
    ports:
      - "${WEB_PORT}:7878"
    volumes:
      - "${CONFIG_PATH}:/config"
      - "${MOVIES_PATH}:/movies"
    environment:
      - TZ=${TZ}
      - API_KEY=${API_KEY}
      - PUBLIC_URL=${PUBLIC_URL}
x-fjord:
  info:
    web_port: "${WEB_PORT}"
  variables:
    - name: WEB_PORT
      type: port
      default: "17878"
    - name: CONFIG_PATH
      type: path
      default: /containers/radarr/config
    - name: MOVIES_PATH
      type: path
      label: Movie library
    - name: TZ
      type: string
      default: UTC
    - name: API_KEY
      type: secret
      default: changeme
    - name: PUBLIC_URL
      type: public_url
      optional: true
`

func TestHandleAppPlanDefaultsAndFolderSets(t *testing.T) {
	root := t.TempDir()
	mgr := stack.NewManager(filepath.Join(root, "stacks"))
	appDataDir := filepath.Join(root, "containers")
	if err := updateSettings(root, func(st *savedSettings) {
		st.AppData = []string{appDataDir}
		st.FolderSets = []FolderSet{
			{ID: "movies", Name: "Movies", Folders: []string{"/tank/movies", "/tank/4k"}, Match: "MOVIE|FILM"},
		}
	}); err != nil {
		t.Fatal(err)
	}
	cat := seedTestCatalog(t, root, "daemonless", []map[string]any{
		{
			"id":   "radarr",
			"name": "Radarr",
			"variants": []map[string]any{
				{"id": "pkg", "label": "pkg", "default": true, "image": "ghcr.io/daemonless/radarr:pkg"},
				{"id": "latest", "label": "latest", "default": false, "image": "ghcr.io/daemonless/radarr:latest"},
			},
		},
	}, map[string]string{"radarr": radarrPlanManifest})

	be := &planTestBackend{
		caps: engine.Capabilities{
			RestartPolicies: []string{"no", "on-failure", "always", "unless-stopped"},
			RestartOnExit:   true,
		},
	}
	srv := &server{
		manager:   mgr,
		backends:  map[string]engine.Backend{"podman": be},
		defEngine: "podman",
		cat:       cat,
		fjordRoot: root,
	}
	mux := http.NewServeMux()
	srv.routes(mux)

	req := httptest.NewRequest(http.MethodGet, "http://nas.local:3567/api/apps/radarr/plan", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var plan installPlan
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Name != "radarr" || plan.AppID != "radarr" || plan.Engine != "podman" || plan.Tag != "latest" {
		t.Fatalf("unexpected metadata: name=%q app=%q engine=%q tag=%q", plan.Name, plan.AppID, plan.Engine, plan.Tag)
	}
	if plan.Values["CONFIG_PATH"] != filepath.Join(appDataDir, "radarr/config") {
		t.Errorf("CONFIG_PATH = %q, want %q", plan.Values["CONFIG_PATH"], filepath.Join(appDataDir, "radarr/config"))
	}
	if !reflect.DeepEqual(plan.Paths["MOVIES_PATH"], []string{"/tank/movies", "/tank/4k"}) || plan.DefaultedFrom["MOVIES_PATH"] != "Movies" {
		t.Errorf("MOVIES_PATH paths=%v defaultedFrom=%v", plan.Paths["MOVIES_PATH"], plan.DefaultedFrom)
	}
	if plan.Values["API_KEY"] != "" {
		t.Errorf("made-up secret API_KEY should start empty in plan, got %q", plan.Values["API_KEY"])
	}
	if plan.Values["PUBLIC_URL"] != "http://nas.local:17878" {
		t.Errorf("PUBLIC_URL = %q, want http://nas.local:17878", plan.Values["PUBLIC_URL"])
	}
	if plan.Restart == nil || plan.Restart.Policy != "unless-stopped" {
		t.Errorf("restart = %+v, want unless-stopped", plan.Restart)
	}
	if !reflect.DeepEqual(plan.NetworkModes, map[string]string{"radarr": "bridge"}) {
		t.Errorf("networkModes = %v, want radarr:bridge", plan.NetworkModes)
	}

	// Save a first copy named "radarr" and disable the homelab plugin: a second
	// plan must advance to "radarr-2", re-root CONFIG_PATH to radarr-2, and
	// stop auto-applying the Movies folder set.
	if err := mgr.Save(&stack.Stack{Name: "radarr", Compose: "services:\n  radarr:\n    image: x\n"}); err != nil {
		t.Fatal(err)
	}
	if err := updateSettings(root, func(st *savedSettings) {
		st.DisabledPlugins = []string{"homelab"}
	}); err != nil {
		t.Fatal(err)
	}
	req2 := httptest.NewRequest(http.MethodGet, "http://nas.local:3567/api/apps/radarr/plan", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second plan status = %d: %s", rec2.Code, rec2.Body.String())
	}
	var plan2 installPlan
	if err := json.Unmarshal(rec2.Body.Bytes(), &plan2); err != nil {
		t.Fatal(err)
	}
	if plan2.Name != "radarr-2" {
		t.Errorf("second copy name = %q, want radarr-2", plan2.Name)
	}
	if plan2.Values["CONFIG_PATH"] != filepath.Join(appDataDir, "radarr-2/config") {
		t.Errorf("second copy CONFIG_PATH = %q", plan2.Values["CONFIG_PATH"])
	}
	if plan2.Values["MOVIES_PATH"] != filepath.Join(appDataDir, "radarr-2/movies") || len(plan2.DefaultedFrom) != 0 {
		t.Errorf("homelab off: MOVIES_PATH = %q, defaultedFrom = %v", plan2.Values["MOVIES_PATH"], plan2.DefaultedFrom)
	}
}

const immichPlanManifest = `name: immich
services:
  immich-server:
    image: ghcr.io/daemonless/immich-server:${IMMICH_TAG:-latest}
    network_mode: host
  database:
    image: ghcr.io/daemonless/postgres:16
    network_mode: host
x-fjord:
  info:
    class: stack
    web_port: "2283"
  networking:
    immich-server: default
    "*": private
  variables:
    - name: IMMICH_TAG
      type: image_tag
      image: ghcr.io/daemonless/immich-server
      default: latest
`

func TestHandleAppPlanMultiServiceNetworking(t *testing.T) {
	root := t.TempDir()
	mgr := stack.NewManager(filepath.Join(root, "stacks"))
	// Existing stack "other" owns "other_priv", so labelPrivateNetworks marks it Private.
	if err := mgr.Save(&stack.Stack{Name: "other", Compose: "services:\n  a:\n    image: x\n"}); err != nil {
		t.Fatal(err)
	}
	cat := seedTestCatalog(t, root, "daemonless", []map[string]any{
		{
			"id":    "immich",
			"name":  "Immich",
			"class": "stack",
			"variants": []map[string]any{
				{"id": "pkg", "label": "pkg", "default": true, "image": "ghcr.io/daemonless/immich-server:pkg"},
			},
		},
	}, map[string]string{"immich": immichPlanManifest})

	be := &planTestBackend{
		nets: []engine.Network{
			{Name: "other_priv", Driver: "bridge"},
			{Name: "ajnet", Driver: "bridge"},
			{Name: "lan", Driver: "epair", Subnet: "192.168.1.0/24"},
		},
	}
	// Only conflists get non-"engine" AddressSource unless hostnet knows them;
	// simulate a LAN conflist using hostnet.UseConflists.
	cniDir := filepath.Join(root, "cni")
	if err := os.MkdirAll(cniDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hostnet.UseConflists(cniDir))
	if err := os.WriteFile(filepath.Join(cniDir, "lan.conflist"), []byte(`{"name":"lan","plugins":[{"type":"epair","bridge":"bridge0","ipam":{"type":"dhcp"}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{
		manager:   mgr,
		backends:  map[string]engine.Backend{"podman": be},
		defEngine: "podman",
		cat:       cat,
		fjordRoot: root,
	}
	mux := http.NewServeMux()
	srv.routes(mux)

	// 1. With a joinable LAN network, immich-server gets [lan, private] and database gets [private].
	req := httptest.NewRequest(http.MethodGet, "http://nas.local:3567/api/apps/immich/plan", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var plan installPlan
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Tag != "" {
		t.Errorf("stack tag must be empty, got %q", plan.Tag)
	}
	if plan.Values["IMMICH_TAG"] != "pkg" {
		t.Errorf("IMMICH_TAG = %q, want pkg", plan.Values["IMMICH_TAG"])
	}
	wantAtts := []composepkg.Attachment{
		{Network: "lan", Service: "immich-server"},
		{Network: "private", Service: "immich-server"},
		{Network: "private", Service: "database"},
	}
	if !reflect.DeepEqual(plan.Networks, wantAtts) || len(plan.NetworkModes) != 0 {
		t.Errorf("LAN plan: networks=%+v modes=%v", plan.Networks, plan.NetworkModes)
	}

	// 2. When the operator configured "bridge" as the default network for podman,
	// "default" resolves to how the stack ships (host mode for immich).
	if err := updateSettings(root, func(st *savedSettings) {
		st.DefaultNetworkFor = map[string]string{"podman": "bridge"}
	}); err != nil {
		t.Fatal(err)
	}
	req2 := httptest.NewRequest(http.MethodGet, "http://nas.local:3567/api/apps/immich/plan", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	var plan2 installPlan
	if err := json.Unmarshal(rec2.Body.Bytes(), &plan2); err != nil {
		t.Fatal(err)
	}
	wantModes := map[string]string{"immich-server": "host", "database": "host"}
	if len(plan2.Networks) != 0 || !reflect.DeepEqual(plan2.NetworkModes, wantModes) {
		t.Errorf("bridge default on host-net stack: networks=%+v modes=%v", plan2.Networks, plan2.NetworkModes)
	}
}

func TestHandleAppPlanChoicesAndFreePorts(t *testing.T) {
	root := t.TempDir()
	mgr := stack.NewManager(filepath.Join(root, "stacks"))
	const choiceManifest = `services:
  app:
    image: ghcr.io/daemonless/app:latest
    ports:
      - "${WEB_PORT}:3000"
      - "${DNS_PORT}:53/udp"
  ml:
    image: ghcr.io/daemonless/ml:latest
x-fjord:
  info:
    web_port: "${WEB_PORT}"
  variables:
    - name: WEB_PORT
      type: port
      default: "3000"
    - name: DNS_PORT
      type: port
      default: "53"
  choices:
    - id: database
      kind: database
      label: Database
      default: sqlite
      options:
        - id: sqlite
          label: SQLite
        - id: external
          label: Your own
          ask:
            - name: DB_KIND
              values:
                postgres: PostgreSQL
            - name: DB_HOST
              default: db.internal
        - id: postgres
          label: PostgreSQL
          services: |
            postgres:
              image: ghcr.io/daemonless/postgres:17
    - id: ml
      kind: part
      label: ML
      default: "on"
      options:
        - id: "on"
          label: With
        - id: "off"
          label: Without
          drop: [ml]
`
	cat := seedTestCatalog(t, root, "daemonless", []map[string]any{
		{"id": "app", "name": "App"},
	}, map[string]string{"app": choiceManifest})

	be := &planTestBackend{
		used: map[string]string{
			"3000/tcp": "other-app",
			"53/udp":   "unbound",
		},
	}
	srv := &server{
		manager:   mgr,
		backends:  map[string]engine.Backend{"podman": be, "appjail": be},
		defEngine: "podman",
		cat:       cat,
		fjordRoot: root,
	}
	mux := http.NewServeMux()
	srv.routes(mux)

	req := httptest.NewRequest(http.MethodGet, "http://nas.local:3567/api/apps/app/plan?choice=database=external&choice=ml=off", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var plan installPlan
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Values["DB_KIND"] != "postgres" || plan.Values["DB_HOST"] != "db.internal" {
		t.Errorf("ask defaults: DB_KIND=%q DB_HOST=%q", plan.Values["DB_KIND"], plan.Values["DB_HOST"])
	}
	if _, hasML := plan.Interfaces["ml"]; hasML {
		t.Errorf("dropped service ml still in interfaces: %v", plan.Interfaces)
	}
	if plan.Values["WEB_PORT"] != "3001" || !strings.Contains(plan.PortNotes["WEB_PORT"], "3001") {
		t.Errorf("unprivileged port 3000 should advance to 3001: val=%q note=%q", plan.Values["WEB_PORT"], plan.PortNotes["WEB_PORT"])
	}
	if plan.Values["DNS_PORT"] != "53" || !strings.Contains(plan.PortNotes["DNS_PORT"], "clients look for this app on 53") {
		t.Errorf("privileged port 53 should stay 53 with note: val=%q note=%q", plan.Values["DNS_PORT"], plan.PortNotes["DNS_PORT"])
	}

	// AppJail guard: postgres option adds a service with no appjail block, so
	// ?engine=appjail&choice=database=postgres falls back to sqlite.
	reqAJ := httptest.NewRequest(http.MethodGet, "http://nas.local:3567/api/apps/app/plan?engine=appjail&choice=database=postgres", nil)
	recAJ := httptest.NewRecorder()
	mux.ServeHTTP(recAJ, reqAJ)
	var planAJ installPlan
	if err := json.Unmarshal(recAJ.Body.Bytes(), &planAJ); err != nil {
		t.Fatal(err)
	}
	if planAJ.Choices["database"] != "sqlite" {
		t.Errorf("appjail choice fallback: got %q, want sqlite", planAJ.Choices["database"])
	}
}

func TestHandleAppPlanInstallsDirectly(t *testing.T) {
	root := t.TempDir()
	mgr := stack.NewManager(filepath.Join(root, "stacks"))
	cat := seedTestCatalog(t, root, "daemonless", []map[string]any{
		{"id": "radarr", "name": "Radarr"},
	}, map[string]string{"radarr": radarrPlanManifest})

	be := &planTestBackend{
		caps: engine.Capabilities{
			RestartPolicies: []string{"no", "on-failure", "always", "unless-stopped"},
			RestartOnExit:   true,
		},
	}
	srv := &server{
		manager:   mgr,
		backends:  map[string]engine.Backend{"podman": be},
		defEngine: "podman",
		cat:       cat,
		fjordRoot: root,
	}
	mux := http.NewServeMux()
	srv.routes(mux)

	planReq := httptest.NewRequest(http.MethodGet, "http://nas.local:3567/api/apps/radarr/plan", nil)
	planRec := httptest.NewRecorder()
	mux.ServeHTTP(planRec, planReq)
	if planRec.Code != http.StatusOK {
		t.Fatalf("plan status = %d: %s", planRec.Code, planRec.Body.String())
	}

	// Pre-create the per-stack directories so MkdirOwned adopts them without
	// requiring root to lchown(1000, 1000) in unprivileged unit tests.
	for _, sub := range []string{"config", "movies"} {
		if err := os.MkdirAll(filepath.Join(root, "containers", "radarr", sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Send the plan JSON straight to POST /api/apps/install.
	instReq := httptest.NewRequest(http.MethodPost, "http://nas.local:3567/api/apps/install", bytes.NewReader(planRec.Body.Bytes()))
	instRec := httptest.NewRecorder()
	mux.ServeHTTP(instRec, instReq)
	if instRec.Code != http.StatusOK {
		t.Fatalf("install status = %d: %s", instRec.Code, instRec.Body.String())
	}
	st, err := mgr.Get("radarr")
	if err != nil {
		t.Fatalf("installed stack not found: %v", err)
	}
	env := st.EnvMap()
	if env["WEB_PORT"] != "17878" || env["PUBLIC_URL"] != "http://nas.local:17878" || len(env["API_KEY"]) != 64 {
		t.Errorf("installed env = %v", env)
	}
}
