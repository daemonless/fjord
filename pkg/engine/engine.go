// Package engine defines the runtime abstraction fjord drives stacks through.
//
// The contract: a stack's definition format is compose YAML (stack.Stack) --
// that is fjord's stack language, not a runtime detail. Each Backend interprets
// it for its runtime (podman today; AppJail/Docker later would translate
// compose services into their own units). Everything above this interface --
// HTTP handlers, the WebSocket exec bridge, the UI -- must stay runtime-
// agnostic: no runtime command strings, no runtime naming conventions, no
// platform mount syntax. Runtime knowledge lives only in the backend packages,
// and anything the UI shows about the runtime (container names, the commands
// being run, volume flavors) is *reported by* the backend, never assumed.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/daemonless/fjord/pkg/stack"
)

// ErrInUse marks a resource (volume, network) that can't be removed because
// something is using it. Backends wrap it around their runtime's specific
// error; the generic handler maps it to HTTP 409 so the UI can offer a force
// option instead of leaking the runtime's raw response.
var ErrInUse = errors.New("resource in use")

// Port is one published port mapping of a running container.
type Port struct {
	HostPort      int    `json:"hostPort"`
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol,omitempty"`
}

// ContainerStatus describes a single container observed via the runtime's
// status API. Ports are the container's ACTUAL published ports -- the source
// of truth for "where is this app reachable", which the saved compose is not
// (edits only apply on recreate).
type ContainerStatus struct {
	Name  string `json:"name"`
	State string `json:"state"` // running | stopped | starting | crashed | ...
	Ports []Port `json:"ports,omitempty"`
	// Detail explains a non-running state in one line (e.g. "crash-looping:
	// see Logs") -- shown as a hint, never parsed.
	Detail string `json:"detail,omitempty"`
	// Addresses is the container's IP on EACH network it is on, keyed by
	// network name. A container on two is the case Address cannot describe:
	// it holds one, and "the first network that answered" picked the private
	// segment over the LAN, so the link offered to open the app pointed at an
	// address no browser can reach.
	Addresses map[string]string `json:"addresses,omitempty"`
	// Address is the container's own IP on an attachable network, when it has
	// one. Reported by the backend because the compose records an address only
	// when the user pinned it: an auto-assigned one exists solely at runtime.
	Address string `json:"address,omitempty"`
}

// StackStatus is the aggregate lifecycle state of a stack's containers.
// State is one of "running" (all containers up), "partial" (some up),
// "stopped" (none up, but known), or "unknown" (status API unreachable).
type StackStatus struct {
	State      string            `json:"state"`
	Containers []ContainerStatus `json:"containers"`
}

// Network describes a container network a stack can attach to so it gets its
// own routable IP (avoiding host port collisions on busy hosts).
type Network struct {
	Name    string `json:"name"`
	Driver  string `json:"driver"`
	Subnet  string `json:"subnet,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	// UsedBy names what depends on this network: what an engine reports as
	// attached right now, plus the stacks whose own configuration puts them
	// on it. A network with either must not be deletable from the UI --
	// removing it strands an attached container on an address nothing can
	// route or clean up, and leaves a stopped stack naming a network that no
	// longer exists.
	//
	// The second half is the daemon's to fill in: an engine can only answer
	// for what is running, and a stopped stack is attached to nothing.
	UsedBy []string `json:"usedBy,omitempty"`
	// Private marks a segment fjord made FOR one stack: nothing outside this
	// host reaches it, and it exists only because that stack needed somewhere
	// to put its database. Not a thing anyone chose from a list, so pickers
	// leave it out -- one per multi-service stack adds up fast.
	//
	// Not derivable from AddressSource: appjail's own networks report
	// "engine", podman's report "pool", and "pool" is also what a perfectly
	// ordinary LAN network with a range reports.
	Private bool `json:"private,omitempty"`
	// OwnedBy names the stack a private segment was made for.
	//
	// A private network is hidden from the pickers because it is SOMEONE
	// ELSE'S -- but a stack's own segment is exactly where its parts belong,
	// and hiding it there made moving a service off it a one-way door: the
	// option vanished the moment anything else was chosen.
	//
	// Stated absolutely rather than relative to a ?stack= query. As a flag
	// meaning "yours", it was only set on the scoped request -- and the UI
	// fires an unscoped one too, so whichever landed last decided whether the
	// option existed at all.
	OwnedBy string `json:"ownedBy,omitempty"`
	// Problem is why the runtime will not use this network, when it is defined
	// on the host but rejected. Such a network has to stay visible: fjord
	// wrote it, so fjord has to let you see and remove it.
	Problem string `json:"problem,omitempty"`
	// Engines names every engine that can attach a stack to this network.
	// A LAN network is a property of the host -- a bridge both runtimes can
	// hang an interface off -- not of one engine, so listing it per engine
	// made one network look like two.
	Engines []string `json:"engines,omitempty"`
	// Subnet6/Gateway6 are the IPv6 half of the segment, when there is one.
	Subnet6  string `json:"subnet6,omitempty"`
	Gateway6 string `json:"gateway6,omitempty"`
	// AddressSource says WHO allocates on this network, which is the only
	// thing that decides whether a stack must bring its own address:
	//
	//	"dhcp"   the segment's DHCP server leases one
	//	"pool"   the runtime's IPAM, from a range in the conflist -- appjail
	//	         cannot ask it, so a jail here needs an address
	//	"static" nothing does; every stack brings its own, on either engine
	//	"engine" the engine's own IPAM (an appjail virtualnet, podman's
	//	         bridge) -- it allocates, so no address is needed
	//
	// Reported rather than inferred. Inferring it from "has a subnet" broke
	// the moment a DHCP network started recording its segment, and told
	// people a virtualnet needed an address when appjail allocates on it.
	AddressSource string `json:"addressSource,omitempty"`
	// Static marks a network that allocates nothing: a stack joining it must
	// bring its own address, on either engine. The form needs this to know
	// the address field is required rather than optional.
	Static bool `json:"static,omitempty"`
	// Bridge is the host interface this network hangs off, when it has one.
	// Two networks on one bridge are two names for one segment: joining both
	// is pointless everywhere and impossible on appjail, so the picker needs
	// to be able to see it rather than finding out at save time.
	Bridge string `json:"bridge,omitempty"`
}

// NetworkKind is one shape of network a backend can create, and which spec
// fields that shape uses. The UI builds its form from this rather than
// branching on an engine name: the two runtimes genuinely differ (a LAN
// network needs a parent interface and a gateway; a NAT network derives its
// own gateway and has no parent), so a field that is required for one is
// meaningless for the other.
type NetworkKind struct {
	ID string `json:"id"`
	// Engine is which backend creates this kind. Set by the daemon when it
	// merges every engine's kinds into one list; a backend leaves it empty.
	Engine string `json:"engine,omitempty"` // "lan" | "nat"
	Label  string `json:"label"`            // what the form calls it, e.g. "epair"
	// Help is one line explaining what a container on this network gets.
	Help string `json:"help,omitempty"`
	// ParentLabel names what Parent must be for this runtime ("Bridge" on
	// FreeBSD, "Interface" on Linux); empty when the kind takes no parent.
	ParentLabel string `json:"parentLabel,omitempty"`
	// ParentSetups are the ways to provision a parent on this platform, each
	// a shell snippet the UI renders verbatim. They come from the backend
	// because the commands are platform-specific.
	ParentSetups []ParentSetup `json:"parentSetups,omitempty"`
	// ParentInterfaces are the host interfaces those setups can be built on.
	ParentInterfaces []ParentInterface `json:"parentInterfaces,omitempty"`
	// Engines are every backend that can make this kind. Engine is the one
	// that will, when the user does not say otherwise.
	Engines []string `json:"engines,omitempty"`
	// Shared marks a kind whose result belongs to the HOST rather than to the
	// engine that created it, so every engine can attach to the same one.
	//
	// A LAN network is shared: it is a bridge on this host, and both runtimes
	// hang an interface off it. A private network is not: podman's is a CNI
	// bridge from `podman network create`, appjail's is a virtualnet from
	// `appjail network add`, and neither can attach to the other's. Merging
	// those two into one row had the form claim both engines and then silently
	// pick one -- so "Private network" gave you an appjail-only or a
	// podman-only network depending on which engine was listed first.
	Shared       bool `json:"shared,omitempty"`
	NeedsGateway bool `json:"needsGateway,omitempty"`
	// AddressNote says where addresses come from when DHCP is not on offer.
	// Without it the radio is simply absent, which reads as something fjord
	// forgot rather than something the kind cannot have.
	AddressNote string `json:"addressNote,omitempty"`
	// SupportsDHCP: addresses can come from the segment's own DHCP server
	// instead of a range podman's IPAM allocates from. When available it is
	// the better
	// default: one allocator instead of two on the same wire.
	SupportsDHCP        bool `json:"supportsDhcp,omitempty"`
	SupportsMTU         bool `json:"supportsMtu,omitempty"`
	SupportsRange       bool `json:"supportsRange,omitempty"`
	SupportsDescription bool `json:"supportsDescription,omitempty"`
}

// ParentSetup is one way to make a parent interface: a label, the commands,
// and anything the user should know before running them.
//
// A backend picks defaults from what the host looks like, but the host cannot
// say which NIC is cabled to which segment or which VLAN the switch tags.
// Inputs lets the UI offer those choices back and ask for a re-render.
type ParentSetup struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Snippet string `json:"snippet"`
	Note    string `json:"note,omitempty"`
	// Inputs the user may vary: "interface", "vlan". Empty = fixed snippet.
	Inputs []string `json:"inputs,omitempty"`
	// What this rendering used, so the UI seeds its controls with it. An
	// empty VLAN means untagged; "auto" on the way in asks for a free id.
	Interface string `json:"interface,omitempty"`
	VLAN      string `json:"vlan,omitempty"`
}

// ParentInterface is a host interface a parent can be built on, with what the
// host knows about it -- the user supplies what it knows nothing about.
type ParentInterface struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"` // "192.168.4.103, default route" / "no address"
	Uplink bool   `json:"uplink,omitempty"`
}

// NetworkSetupper re-renders a ParentSetup with the user's choices. Optional:
// fjordd type-asserts it, and a backend without it just serves the defaults
// from Capabilities().
type NetworkSetupper interface {
	ParentSetup(ctx context.Context, kind, nic, vlan string) (ParentSetup, error)
}

// NetworkSpec describes a network to create in runtime-neutral terms; the
// backend translates it (a CNI conflist on FreeBSD podman, `appjail network
// add` on appjail). Mirrors VolumeSpec.
type NetworkSpec struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // a NetworkKind.ID from Capabilities
	// Parent is the host interface the network hangs off -- a bridge on
	// FreeBSD, a NIC or VLAN subinterface on Linux. Empty for kinds whose
	// ParentLabel is empty.
	Parent string `json:"parent,omitempty"`
	// AddressSource is "dhcp" (the segment's DHCP server allocates) or "pool"
	// / "" (this host allocates from Subnet). A dhcp network needs no subnet,
	// gateway or range: the lease carries them.
	AddressSource string `json:"addressSource,omitempty"`
	// Subnet6/Gateway6 are the IPv6 half, when the network has one. Optional
	// and independent: a network may be v4-only, dual-stack, or v6-only.
	//
	// Only a pool or static network can carry one. A DHCP network's addresses
	// come from the CNI dhcp plugin, which is IPv4-only, and one plugin cannot
	// run two IPAMs -- its v6 would have to come from SLAAC, which the epair
	// plugin does not do.
	Subnet6  string `json:"subnet6,omitempty"`
	Gateway6 string `json:"gateway6,omitempty"`
	// For restricts a network to one engine: "" means any. The network is the
	// same object either way -- this records which engine's abilities the form
	// was filled in for, so a range network made for podman is not offered to
	// appjail, which cannot ask host-local for an address.
	For         string `json:"for,omitempty"`
	Subnet      string `json:"subnet"`
	Gateway     string `json:"gateway,omitempty"`
	MTU         int    `json:"mtu,omitempty"`
	RangeStart  string `json:"rangeStart,omitempty"`
	RangeEnd    string `json:"rangeEnd,omitempty"`
	Description string `json:"description,omitempty"`
}

// NetworkParent is a host interface a "lan" network can attach to.
// InUse marks one that already backs a network, so the UI can say so without
// forbidding it (several networks on one bridge is legal).
type NetworkParent struct {
	Name  string `json:"name"`
	InUse bool   `json:"inUse,omitempty"`
	// Subnet, Gateway and HostIP are what the host already knows about the
	// segment this parent is on, so the form can prefill instead of asking for
	// facts the machine can read. Empty when nothing on the parent carries an
	// address -- then the user supplies them.
	Subnet  string `json:"subnet,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	HostIP  string `json:"hostIp,omitempty"`
}

// Volume is a runtime-managed named volume. Kind is the backend's
// classification ("local", "nfs", ...); Anonymous marks runtime-generated
// volumes (e.g. podman's 64-hex container volumes) the UI hides by default.
// Options carries raw driver detail for display.
type Volume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Kind       string            `json:"kind,omitempty"`
	Anonymous  bool              `json:"anonymous,omitempty"`
	Mountpoint string            `json:"mountpoint"`
	Options    map[string]string `json:"options,omitempty"`
	CreatedAt  string            `json:"createdAt,omitempty"`
}

// VolumeSpec describes a volume to create, in runtime-neutral terms: the
// backend translates Kind+Server+Path into its platform's mount options (e.g.
// FreeBSD mount_nfs wants device="server:/path" while Linux wants
// o="addr=server"). Options, when set, is an advanced raw-passthrough that
// overrides the translation.
type VolumeSpec struct {
	Name     string            `json:"name"`
	Kind     string            `json:"kind,omitempty"` // "" or "local" | "nfs" | "smb"
	Server   string            `json:"server,omitempty"`
	Path     string            `json:"path,omitempty"`     // nfs export path, or smb share name
	User     string            `json:"user,omitempty"`     // smb: account ("guest" when empty)
	Password string            `json:"password,omitempty"` // smb: stored by the backend, never echoed
	ReadOnly bool              `json:"readOnly,omitempty"`
	Driver   string            `json:"driver,omitempty"`
	Options  map[string]string `json:"options,omitempty"`
}

// ExecOptions configures an interactive shell session into a container.
type ExecOptions struct {
	Container string   // the container (a stack service's) to exec into
	Cmd       []string // shell command; defaults to ["/bin/sh"] when empty
}

// ExecSession is a live bidirectional shell into a container -- deliberately
// backend-agnostic so the WebSocket transport and xterm UI never touch podman.
// A future AppJail/Docker backend implements this the same way. Read yields the
// container's output; Write sends the user's keystrokes; Resize adjusts the TTY.
type ExecSession interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

// Descriptor is an engine's self-description: its identity, how to detect
// whether it can run on this host, and how to build its backend. All
// engine-specific knowledge (name, description, availability checks, version
// caveats, install package) lives with the engine and is surfaced through
// this -- the generic daemon code just iterates descriptors and never names a
// specific engine. Registering an engine = adding its Descriptor to the list.
type Descriptor struct {
	Name        string
	Description string
	Package     string // OS package that provides it (for host-mode install)
	// Available reports whether the engine can run here: ok, a reason when it
	// can't, and a non-fatal warning when it can but with caveats (e.g. an old
	// version). ok=false with reason means unavailable; ok=true with warning
	// means usable-with-a-caveat.
	Available func() (ok bool, reason, warning string)
	New       func() Backend
}

// Unmanaged is a container the engine runs that no fjord stack owns -- one
// started by hand or by another tool -- with what it takes to adopt it as a
// stack: podman hands over the argv it was created from (RunArgs), appjail
// converts the jail itself (Spec). Unadoptable, when set, says why neither
// is possible. Project names a runtime-side grouping made outside fjord (a
// director project), so the handler can tell fjord's own jails apart.
type Unmanaged struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Image       string     `json:"image"`
	State       string     `json:"state"`
	Project     string     `json:"project,omitempty"`
	RunArgs     []string   `json:"-"`
	Spec        *AdoptSpec `json:"-"`
	Unadoptable string     `json:"-"`
}

// AdoptSpec is the stack an unmanaged container becomes: a compose file and
// .env always (the UI, status and the Open link read those), plus the
// director bundle for a jail on the appjail engine.
type AdoptSpec struct {
	Service  string
	Compose  string
	Env      string
	Director string // appjail-director.yml
	Makejail string
	Template string // template.conf: the jail parameters
	Notes    []string
}

// DiskRow is one category of the runtime's disk-usage report (podman system df):
// how much space it holds and how much is reclaimable. Human strings are for
// display; RawReclaimable is bytes for sizing/decisions.
type DiskRow struct {
	Type           string `json:"type"` // "Images" | "Containers" | "Local Volumes" | ...
	Total          int    `json:"total"`
	Active         int    `json:"active"`
	Size           string `json:"size"`
	Reclaimable    string `json:"reclaimable"`
	RawReclaimable int64  `json:"rawReclaimable"`
	// Note explains something Prune can't act on -- e.g. images pinned by
	// containers the runtime doesn't own (buildah builds, appjail jails) --
	// so "in use" numbers that never move don't read as a failed prune.
	Note string `json:"note,omitempty"`
}

// PruneOptions selects what a Prune should reclaim. Empty = nothing. Each
// backend honors only what its PruneCapabilities report; the rest is ignored.
type PruneOptions struct {
	Containers bool `json:"containers"` // stopped containers
	Images     bool `json:"images"`     // dangling images
	AllImages  bool `json:"allImages"`  // ALL images not used by a container (implies Images)
	Volumes    bool `json:"volumes"`    // volumes not used by any container
	Networks   bool `json:"networks"`   // networks no container is attached to
	Build      bool `json:"build"`      // build leftovers: working containers, build cache
}

// PruneCapabilities says which PruneOptions a backend can act on, so the UI
// offers only what the selected engine supports.
type PruneCapabilities struct {
	Containers bool `json:"containers"`
	Images     bool `json:"images"`
	AllImages  bool `json:"allImages"`
	Volumes    bool `json:"volumes"`
	Networks   bool `json:"networks"`
	Build      bool `json:"build"`
}

// Capabilities reports optional features a backend supports beyond the core
// lifecycle, so generic code branches on a capability instead of an engine
// name. Mirrors PruneCapabilities.
type Capabilities struct {
	// RemoteVolumes: the engine can mount nfs:// / smb:// folders as named
	// volumes (and store SMB credentials for them).
	RemoteVolumes bool `json:"remoteVolumes"`
	// NetworkKinds are the networks this engine can create. Empty means
	// CreateNetwork/RemoveNetwork are unsupported and the UI offers neither.
	NetworkKinds []NetworkKind `json:"networkKinds,omitempty"`
	// NetworkRemove: this engine can delete a network. False where networks
	// are defined elsewhere -- offering Delete would only ever error.
	NetworkRemove bool `json:"networkRemove"`
	// NetworkNote says why NetworkKinds is empty, so the UI can explain the
	// absence instead of silently hiding a button. Empty when creating works.
	NetworkNote string `json:"networkNote,omitempty"`
	// UpdateServices: Update can pull and recreate a subset of a stack's
	// services, leaving the rest running untouched.
	UpdateServices bool `json:"updateServices"`
}

// PruneReport summarizes a prune run: a human total and the raw command output.
type PruneReport struct {
	Reclaimed string `json:"reclaimed"` // e.g. "165.7GB" ("" if unknown)
	Output    string `json:"output"`
}

// Backend defines the execution engine interface.
// By coding to this interface, FJORD can easily swap out Podman for Docker or AppJail later.
type Backend interface {
	// Up spins up a stack and returns an io.ReadCloser streaming the terminal output.
	Up(ctx context.Context, s *stack.Stack) (io.ReadCloser, error)
	// Down tears down a stack and returns an io.ReadCloser streaming the terminal output.
	Down(ctx context.Context, s *stack.Stack) (io.ReadCloser, error)
	// Update pulls the latest images for a stack then recreates it, streaming
	// both steps. services limits both to those services (empty = all); an
	// engine without Capabilities().UpdateServices refuses a non-empty list.
	Update(ctx context.Context, s *stack.Stack, services []string) (io.ReadCloser, error)
	// Restart restarts a stack's containers in place (no pull, no recreate).
	Restart(ctx context.Context, s *stack.Stack) (io.ReadCloser, error)
	// Logs streams container logs -- all of the stack's containers merged, or
	// just the given subset (names from Status; empty = all). With follow it
	// tails live until ctx is cancelled; tail bounds the backlog.
	Logs(ctx context.Context, s *stack.Stack, tail int, follow bool, containers []string) (io.ReadCloser, error)
	// Exec opens an interactive shell session in a container. Only session
	// creation is runtime-specific; the WebSocket bridge above is generic.
	Exec(ctx context.Context, opts ExecOptions) (ExecSession, error)
	// Status reports the current lifecycle state of a stack's containers.
	Status(ctx context.Context, s *stack.Stack) (StackStatus, error)
	// Networks lists container networks a stack can attach to for its own IP.
	Networks(ctx context.Context) ([]Network, error)
	// CreateNetwork creates a network per spec. Supported only for the kinds
	// in Capabilities().NetworkKinds.
	CreateNetwork(ctx context.Context, spec NetworkSpec) (Network, error)
	// RemoveNetwork deletes a network. It returns ErrInUse when containers are
	// still attached, unless force is set -- deleting a network out from under
	// a running container leaves it with an address nothing can route.
	RemoveNetwork(ctx context.Context, name string, force bool) error
	// NetworkParents lists host interfaces a "lan" network can hang off.
	// Empty when the engine creates no kind that takes a parent.
	NetworkParents(ctx context.Context) ([]NetworkParent, error)
	// Volumes lists podman-managed named volumes.
	Volumes(ctx context.Context) ([]Volume, error)
	// CreateVolume creates a named volume (local driver, optional NFS opts).
	CreateVolume(ctx context.Context, spec VolumeSpec) (Volume, error)
	// RemoveVolume deletes a named volume; force removes it even if in use.
	RemoveVolume(ctx context.Context, name string, force bool) error
	// UsedPorts maps host ports already published by ANY container (running or
	// created) to the holder's container name -- pre-flight conflict checks.
	// Key format: "8080/tcp".
	UsedPorts(ctx context.Context) (map[string]string, error)
	// DiskUsage reports reclaimable space per category (the runtime's df).
	DiskUsage(ctx context.Context) ([]DiskRow, error)
	// PruneCapabilities reports which prune options this runtime supports.
	PruneCapabilities() PruneCapabilities
	// Prune reclaims space per opts, returning what was freed. A host-wide
	// maintenance op, not tied to a stack.
	Prune(ctx context.Context, opts PruneOptions) (PruneReport, error)
	// ImageRepoDigests returns the "repo@sha256:..." digests a locally-present
	// image is known by (empty if the image isn't pulled). For a multi-arch
	// image this includes the manifest-list/index digest, which is what a
	// registry reports for the tag -- so it's the digest to compare against for
	// update detection. Compare against RepoDigests, never the per-arch .Digest.
	ImageRepoDigests(ctx context.Context, ref string) ([]string, error)
	// RunningImages reports the image each of a stack's services was created
	// from. nil when the engine cannot say, and update checks then fall back to
	// the local tag -- which is right only until anything else pulls it.
	RunningImages(ctx context.Context, s *stack.Stack) ([]RunningImage, error)
	// Capabilities reports optional features (remote volumes, ...) so generic
	// code branches on a capability, not an engine name.
	Capabilities() Capabilities
	// StoreSMBCredentials records the password an smb:// volume will use.
	// Meaningful only when Capabilities().RemoteVolumes; other engines return
	// an error.
	StoreSMBCredentials(server, username, password string) error
}

// RunningImage is the image one service's container was created from.
type RunningImage struct {
	Service string
	ImageID string
	// Digest is the registry digest the image was pulled as -- for a
	// multi-arch image the index digest, which is what a registry reports for
	// a tag. The container keeps it; the image does not: once a pull moves
	// the tag, the old image's RepoDigests is empty.
	Digest string
	// Digests is every registry digest the running image is known by: Digest
	// plus the image's RepoDigests while it still has them. One image gathers
	// several -- a re-pull after an other-arch rebuild adds the new index
	// digest to the image already here, since its bytes did not change.
	Digests []string
}

// HumanBytes formats a byte count the way podman's df does ("35.0GB"), so
// engines that size things themselves match the podman rows.
func HumanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "kMGTPE"[exp])
}
