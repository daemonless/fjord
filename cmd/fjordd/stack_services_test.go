package main

import (
	"strings"
	"testing"

	composepkg "github.com/daemonless/fjord/pkg/compose"

	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"
)

// immich as it runs on netlab: the server on the LAN and on the private
// segment, the other three private only. The stack-level view cannot say any
// of this -- it reports "the stack is on lan and immich_private" and leaves
// which jail is where unanswerable.
const servicesDirector = `options: []
services:
  immich-server:
    name: immich_immich_server
    options:
      - from: ghcr.io/daemonless/immich-server:latest
      - bridge: 'epair:immichsrv bridge:lanbridge'
      - ifconfig: 'sb_immichsrv:192.168.4.90/24'
      - virtualnet: 'immich_private:immichsrvp address:10.99.0.5'
    volumes:
      - immich-data: /data
  redis:
    name: immich_redis
    options:
      - from: ghcr.io/daemonless/redis:latest
      - virtualnet: 'immich_private:immichredis address:10.99.0.3'
  database:
    name: immich_database
    options:
      - from: ghcr.io/daemonless/immich-postgres:latest
      - virtualnet: 'immich_private:immichdb address:10.99.0.2'
    volumes:
      - db-data: /var/lib/postgresql/data
volumes:
  immich-data:
    device: !ENV '${UPLOAD_LOCATION}'
  db-data:
    device: !ENV '${DB_DATA_LOCATION}'
`

func TestStackServicesDirector(t *testing.T) {
	seedNetwork(t)
	st := &stack.Stack{
		Name:     "immich",
		Director: servicesDirector,
		Env:      "UPLOAD_LOCATION=/containers/immich/library\nDB_DATA_LOCATION=/containers/immich/postgres\n",
	}
	status := engine.StackStatus{State: "running", Containers: []engine.ContainerStatus{
		{Name: "immich_immich_server", State: "running", Address: "192.168.4.90"},
		{Name: "immich_redis", State: "running"},
		{Name: "immich_database", State: "stopped", Detail: "crash-looping"},
	}}
	views := stackServices(st, status)
	if len(views) != 3 {
		t.Fatalf("got %d services, want 3: %+v", len(views), views)
	}
	by := map[string]serviceView{}
	for _, v := range views {
		by[v.Name] = v
	}

	srv := by["immich-server"]
	if srv.Image != "ghcr.io/daemonless/immich-server:latest" {
		t.Errorf("image = %q", srv.Image)
	}
	if srv.Container != "immich_immich_server" || srv.State != "running" {
		t.Errorf("runtime half not attached: %+v", srv)
	}
	// Two networks, and the one it is published on is distinguishable from
	// the one it talks to the database over.
	if len(srv.Networks) != 2 {
		t.Fatalf("immich-server networks = %+v, want 2", srv.Networks)
	}
	if srv.Networks[1].Network != "immich_private" || srv.Networks[1].IP != "10.99.0.5" {
		t.Errorf("private attachment wrong: %+v", srv.Networks[1])
	}
	// The volume says where the data actually is, not ${UPLOAD_LOCATION}.
	if len(srv.Volumes) != 1 || srv.Volumes[0].Source != "/containers/immich/library" {
		t.Errorf("volumes = %+v", srv.Volumes)
	}
	if srv.Volumes[0].Dest != "/data" || srv.Volumes[0].Kind != "bind" {
		t.Errorf("volume detail wrong: %+v", srv.Volumes[0])
	}

	db := by["database"]
	if len(db.Networks) != 1 || db.Networks[0].Network != "immich_private" || db.Networks[0].IP != "10.99.0.2" {
		t.Errorf("database networks = %+v", db.Networks)
	}
	if db.State != "stopped" || db.Detail != "crash-looping" {
		t.Errorf("database runtime half wrong: %+v", db)
	}
	if len(db.Volumes) != 1 || db.Volumes[0].Source != "/containers/immich/postgres" {
		t.Errorf("database volumes = %+v", db.Volumes)
	}
	// Each service names its own service, so a row knows what it belongs to.
	for _, v := range views {
		for _, a := range v.Networks {
			if a.Service != v.Name {
				t.Errorf("%s: attachment claims service %q", v.Name, a.Service)
			}
		}
	}
}

// A bundle that puts its networking on the project, which is where it lived
// before fjord wrote it per service, still describes every service.
func TestStackServicesProjectLevelNetworks(t *testing.T) {
	seedNetwork(t)
	st := &stack.Stack{Name: "zensical", Director: `options:
  - bridge: 'epair:zensical bridge:vlan5bridge'
  - ifconfig: 'sb_zensical:192.168.5.5/24'
services:
  zensical:
    name: zensical_zensical
    options:
      - from: ghcr.io/daemonless/zensical:latest
`}
	views := stackServices(st, engine.StackStatus{})
	if len(views) != 1 {
		t.Fatalf("got %d services, want 1", len(views))
	}
	if len(views[0].Networks) != 1 || views[0].Networks[0].IP != "192.168.5.5" {
		t.Errorf("project-level networking not reported: %+v", views[0].Networks)
	}
	if views[0].Networks[0].Service != "zensical" {
		t.Errorf("attachment should name the service it was given to: %+v", views[0].Networks[0])
	}
}

// A plain compose stack answers the same shape, per service.
func TestStackServicesCompose(t *testing.T) {
	st := &stack.Stack{Name: "app", Compose: `services:
  web:
    image: nginx:latest
    networks: [vlan5]
    ports:
      - "8080:80"
    volumes:
      - /containers/app/html:/usr/share/nginx/html:ro
  cache:
    image: redis:latest
    networks: [private]
`}
	views := stackServices(st, engine.StackStatus{State: "running",
		Containers: []engine.ContainerStatus{
			{Name: "app_web_1", State: "running"},
			{Name: "app_cache_1", State: "running"},
		}})
	if len(views) != 2 {
		t.Fatalf("got %d services, want 2", len(views))
	}
	by := map[string]serviceView{}
	for _, v := range views {
		by[v.Name] = v
	}
	if n := by["web"].Networks; len(n) != 1 || n[0].Network != "vlan5" {
		t.Errorf("web networks = %+v", n)
	}
	if n := by["cache"].Networks; len(n) != 1 || n[0].Network != "private" {
		t.Errorf("cache networks = %+v", n)
	}
	// The two disagree, which is exactly what AttachedNetworks gives up on.
	if by["web"].Container != "app_web_1" || by["cache"].Container != "app_cache_1" {
		t.Errorf("containers not matched: %+v", views)
	}
	if v := by["web"].Volumes; len(v) != 1 || !v[0].ReadOnly || v[0].Kind != "bind" {
		t.Errorf("web volumes = %+v", v)
	}
	if p := by["web"].Ports; len(p) != 1 || p[0].HostPort != 8080 {
		t.Errorf("web ports = %+v", p)
	}
}

// The Interface column said "on create" for every row of a stack that had been
// running for days: the stack-level pass named a flat list, and the per-service
// view -- the one the editor actually reads -- was never named at all.
func TestServiceIfacesAreNamedPerService(t *testing.T) {
	st := &stack.Stack{Compose: `services:
  immich-server:
    image: a
    networks:
      - lan
      - immich_priv
  database:
    image: b
    networks:
      - immich_priv
networks:
  lan:
    external: true
  immich_priv:
    external: true
`}
	byName := map[string][]composepkg.Attachment{}
	for _, v := range stackServices(st, engine.StackStatus{}) {
		byName[v.Name] = v.Networks
	}
	got := byName["immich-server"]
	if len(got) != 2 || got[0].Iface != "eth0" || got[1].Iface != "eth1" {
		t.Fatalf("immich-server: %+v", got)
	}
	// Numbering restarts inside each container: the second service's only
	// interface is ITS eth0, not eth2.
	db := byName["database"]
	if len(db) != 1 || db[0].Iface != "eth0" {
		t.Fatalf("database: %+v, want its own eth0", db)
	}
}

// A DHCP lease is reported as live, never as the pin: the editor saves
// whatever sits in the pin, and zensical on netlab came back with its lease
// written into the compose as a fixed ipv4_address.
func TestAttachStatusKeepsLeasesOutOfThePin(t *testing.T) {
	views := []serviceView{{
		Name: "zensical",
		Networks: []composepkg.Attachment{
			{Network: "lan-dhcp", Service: "zensical", MAC: "58:9c:fc:10:7f:7e"},
			{Network: "lan-static", Service: "zensical", IP: "192.168.4.228"},
		},
	}}
	attachStatus(views, engine.StackStatus{Containers: []engine.ContainerStatus{{
		Name: "zensical_zensical_1", State: "running",
		Addresses: map[string]string{"lan-dhcp": "192.168.4.107", "lan-static": "192.168.4.228"},
	}}})
	v := views[0]
	if v.Networks[0].IP != "" {
		t.Errorf("lease became a pin: %q", v.Networks[0].IP)
	}
	if v.Networks[1].IP != "192.168.4.228" {
		t.Errorf("real pin lost: %q", v.Networks[1].IP)
	}
	if v.Live["lan-dhcp"] != "192.168.4.107" {
		t.Errorf("live lan-dhcp = %q, want the lease", v.Live["lan-dhcp"])
	}
}

// zensical on netlab: a DHCP lease on lan-dhcp, its private segment and the
// bridge. The link is the lease; with only pins read it was nothing, and the
// page opened the engine's first address, 10.89.0.6.
func TestLinkHostUsesTheLease(t *testing.T) {
	views := []serviceView{{
		Name: "zensical",
		Networks: []composepkg.Attachment{
			{Network: "lan-dhcp"}, {Network: "zensical_priv"}, {Network: composepkg.Bridge},
		},
		Live: map[string]string{"lan-dhcp": "192.168.4.114", "zensical_priv": "10.100.0.5"},
	}}
	reachable := func(n string) bool { return n == "lan-dhcp" }
	if got := linkHost(views, reachable); got != "192.168.4.114" {
		t.Errorf("linkHost = %q, want the lease 192.168.4.114", got)
	}
	// A pin still counts when nothing is running.
	views[0].Live = nil
	views[0].Networks[0].IP = "192.168.4.50"
	if got := linkHost(views, reachable); got != "192.168.4.50" {
		t.Errorf("stopped, pinned: linkHost = %q, want 192.168.4.50", got)
	}
}

// A stack being brought up says so, and a second operation is refused naming
// the first -- the page reads the one and shows the other.
func TestLockStackAsReportsBusy(t *testing.T) {
	unlock, ok := lockStackAs("t-busy", "installing")
	if !ok {
		t.Fatal("first lock refused")
	}
	if got := busyWith("t-busy"); got != "installing" {
		t.Errorf("busyWith = %q, want installing", got)
	}
	if _, ok := lockStackAs("t-busy", "up"); ok {
		t.Fatal("second operation was not refused")
	}
	if msg := anotherOperation("t-busy"); !strings.Contains(msg, "installing") {
		t.Errorf("refusal does not say what is running: %q", msg)
	}
	unlock()
	if got := busyWith("t-busy"); got != "" {
		t.Errorf("still busy after unlock: %q", got)
	}
}

// A container is its row's by the engine's service label first; a name that
// fits is the fallback for containers made without one.
func TestAttachStatusPrefersServiceLabel(t *testing.T) {
	views := []serviceView{{Name: "immich-server"}, {Name: "redis"}, {Name: "database"}}
	attachStatus(views, engine.StackStatus{Containers: []engine.ContainerStatus{
		{Name: "immich-immich-server-1", Service: "immich-server", State: "running"}, // a name the name rules miss
		{Name: "immich_redis_1", State: "running"},                                   // no label: by name
		{Name: "immich_database_1", Service: "database", State: "exited"},
	}})
	want := map[string]string{"immich-server": "running", "redis": "running", "database": "exited"}
	for _, v := range views {
		if v.State != want[v.Name] {
			t.Errorf("%s: state %q (container %q), want %q", v.Name, v.State, v.Container, want[v.Name])
		}
	}
}
