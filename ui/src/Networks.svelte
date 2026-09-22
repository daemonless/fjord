<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import EmptyState from './EmptyState.svelte';
  import { toast } from './toast';
  import FixSnippet from './FixSnippet.svelte';

  type Network = { name: string; driver: string; subnet?: string; gateway?: string; subnet6?: string; gateway6?: string; usedBy?: string[]; problem?: string; engines?: string[]; addressSource?: string; bridge?: string; private?: boolean; ownedBy?: string };
  // A kind is the engine's own declaration of what it can create and which
  // fields that shape uses -- the form is built from this rather than from
  // anything the UI knows about a specific runtime.
  type Kind = {
    id: string;
    label: string;
    help?: string;
    parentLabel?: string;
    parentSetups?: Setup[];
    parentInterfaces?: { name: string; detail?: string; uplink?: boolean }[];
    needsGateway?: boolean;
    engine?: string;   // which backend makes this kind
    engines?: string[]; // every backend that can
    shared?: boolean;   // result belongs to the host, so any engine can attach
    supportsDhcp?: boolean;
    addressNote?: string;   // where addresses come from when DHCP is not offered
    supportsMtu?: boolean;
    supportsRange?: boolean;
    supportsDescription?: boolean;
  };
  type Setup = {
    id: string;
    label: string;
    snippet: string;
    note?: string;
    inputs?: string[];   // "interface", "vlan" -- what the user may change
    interface?: string;
    vlan?: string;
  };
  type Parent = {
    name: string;
    inUse?: boolean;
    subnet?: string;   // what the host already knows about this segment
    gateway?: string;
    hostIp?: string;
  };

  let networks: Network[] = [];
  let kinds: Kind[] = [];
  // Why creating is unavailable, when it is -- an absent button with no
  // explanation is its own kind of confusing.
  let kindsNote = '';
  // Whether this engine can delete a network at all.
  let canRemove = true;
  let parents: Parent[] = [];
  let loading = true;
  let error = '';

  // Four independent requests that were made one after another, with the page
  // showing a spinner until the last one landed -- so the wait was their sum
  // rather than the slowest of them. They are started together now, and the
  // list stops waiting on the three it does not need: kinds and parents only
  // decide whether creating is offered, and the default only decorates a row.
  async function load() {
    loading = true;
    error = '';
    const host = refreshHost();
    const dflt = fetch('/api/settings/network')
      .then((r) => (r.ok ? r.json() : null))
      .catch(() => null);
    try {
      const res = await fetch('/api/networks');
      if (!res.ok) throw new Error(await res.text());
      networks = await res.json();
    } catch (e: any) {
      error = e.message || 'Failed to load networks';
    } finally {
      loading = false;
    }
    // Advisory: a failure here disables creating but must not hide, or delay,
    // the networks that already exist.
    await host;
    const d = await dflt;
    defaultFor = d?.forEngine ?? {};
  }

  // What the host looks like right now: which bridges exist, and what the
  // setup snippets should suggest next. Re-read whenever the create dialog
  // opens -- it tells the user to go make a bridge and come back, so the
  // answer is expected to have changed since the page loaded.
  async function refreshHost() {
    const [k, p] = await Promise.all([
      fetch('/api/networks/kinds').then((r) => (r.ok ? r.json() : null)).catch(() => null),
      // Unscoped on purpose: a bridge is the HOST's, and both engines answer
      // from the same place. Naming podman here meant the list came back empty
      // the moment podman was turned off in Plugins -- so a host with a bridge
      // sitting right there was told it had none, while the setup snippet
      // below it offered to create "lanbridge2" because it could see the one
      // that already existed.
      fetch('/api/networks/parents').then((r) => (r.ok ? r.json() : [])).catch(() => []),
    ]);
    kinds = k?.kinds ?? [];
    kindsNote = k?.note ?? '';
    canRemove = k?.canRemove ?? true;
    parents = p;
  }

  onMount(load);

  // Deleting goes to the engine that owns the network: a LAN network belongs
  // to podman (it is a conflist), a private one to appjail.
  function engineOf(name: string): string {
    const n = networks.find((x) => x.name === name);
    return n?.engines?.[0] ?? '';
  }

  // ---- create ----
  let creating = false;
  let submitting = false;
  let createError = '';
  let kindID = '';
  let form = { name: '', parent: '', subnet: '', gateway: '', subnet6: '', gateway6: '', mtu: '', rangeStart: '', rangeEnd: '', description: '' };
  // "dhcp" = the segment's own DHCP server allocates; "pool" = a range set
  // aside in the conflist, which podman's host-local IPAM allocates from and
  // appjail cannot draw from at all. Not fjord: it writes the range and never
  // touches it again.
  let addressSource: 'dhcp' | 'pool' | 'static' = 'dhcp';
  // Which engine this network is for. One control, one meaning, in one place
  // on both tabs -- it used to be "Created by" near the top of one tab and
  // "For" near the bottom of the other, so the same question looked like two
  // unrelated settings and the fields moved when you switched tabs.
  //
  // What it MEANS differs by kind, and that is the kind's business, not the
  // control's: a private network really is one engine's own object, so only
  // one engine can be chosen; an epair network is a host bridge both engines
  // can attach to, so "both" is offered and is the default.
  let forEngine = '';
  // Both address sources are offered for every engine. A range once looked
  // like podman's alone -- appjail cannot ask host-local to allocate from it
  // -- but that only means a jail needs its address typed in, which is said
  // under the choice. Hiding it took DHCP with it, since both buttons live in
  // one control, and left the appjail tab with no address choice at all.
  let advanced = false;
  // The bridge-setup help, available whether or not a bridge already exists.
  let showSetup = false;
  // A working copy of the kind's setups: the host guesses the NIC and the VLAN
  // id, the user corrects them, and the backend re-renders that one snippet.
  let setups: Setup[] = [];
  let setupBusy = '';
  let rechecking = false;
  // What new installs start on, per engine. There is no shared value: a
  // network can belong to one engine, so a single default could name one the
  // other cannot attach to -- and the page then had to decide, per row,
  // which of the two a "Default" button meant.
  let defaultFor: Record<string, string> = {};
  // The three states a stack can be in with no network of its own, in podman's
  // words: "bridge" is its NAT bridge, "host" is this host's own stack, "none"
  // is no network at all. Listed here because they are as real a choice as any
  // network the host defines -- and as valid a default for new installs.
  const BUILT_IN = [
    {
      name: 'bridge',
      // Not podman's: every engine has one. podman's is the network it calls
      // "podman", appjail's is its NAT virtualnet -- the same deal either way,
      // and naming one engine made it read as unavailable on the other.
      detail: "The engine's own bridge — a private address behind NAT, reached on the ports it publishes on this host.",
    },
    {
      name: 'host',
      detail: "This host's own stack — no address and no port mapping of its own, so it binds host ports directly.",
      // An appjail director project has no option for it: a jail parameter,
      // not a director option. Shown in the engines column like every other
      // row rather than as a badge of its own.
      not: ['appjail'],
    },
    {
      name: 'none',
      detail: 'No network at all: nothing in and nothing out.',
    },
  ];
  // Every engine that can create networks here, from the kinds they offered.
  $: allEngines = [...new Set(kinds.flatMap((k) => k.engines ?? (k.engine ? [k.engine] : [])))];
  // Reactive, not a const: visibleBuiltIn below is computed from this, and a
  // const closure hid that it reads allEngines. So the row list was decided at
  // first render -- before /api/networks/kinds had answered and allEngines was
  // still empty -- and never recomputed, because the only reactive thing it
  // referenced settled on the value it already had. host and none vanished
  // from the page and only bridge survived, on the strength of being default.
  $: enginesFor = (b: { not?: string[] }) => allEngines.filter((e) => !(b.not ?? []).includes(e));
  // With one engine every row would say the same word, which is noise rather
  // than information. The column earns its place only when there is a choice.
  $: showEngines = allEngines.length > 1;
  // And a built-in no engine here can use is not a row worth showing: host on
  // an appjail-only host is not "unavailable", it is simply not a thing you
  // can pick.
  $: visibleBuiltIn = BUILT_IN.filter((b) => enginesFor(b).length > 0);

  // Networks nothing on the LAN can reach. They go behind a disclosure rather
  // than pushing the real ones down -- one per multi-service stack adds up
  // fast -- but they are not all the same thing, and saying "made by a stack"
  // over all of them was wrong about appjail's own ajnet, which no stack made
  // and which every jail can use.
  //
  //   ownedBy   fjord made this FOR one stack, to hold its database
  //   engine    the engine's own default NAT, shared by everything on it
  const stackOwned = (n: Network) => !!n.ownedBy;
  const engineOwned = (n: Network) => !stackOwned(n) && n.addressSource === 'engine';
  const isPrivateNet = (n: Network) => stackOwned(n) || engineOwned(n);
  $: lanNets = networks.filter((n) => !isPrivateNet(n));
  $: privateNets = networks.filter(isPrivateNet);
  // What the disclosure says it is hiding, without claiming a stack made any
  // of it unless one did.
  $: privateSummary = (() => {
    const owned = privateNets.filter(stackOwned).length;
    const engine = privateNets.length - owned;
    const bits: string[] = [];
    if (owned) bits.push(`${owned} made by a stack`);
    if (engine) bits.push(`${engine} the engine's own`);
    return bits.join(', ');
  })();
  // Whose it is, per row.
  const privateWhose = (n: Network) =>
    stackOwned(n) ? `made by ${n.ownedBy}` : "the engine's own default network";
  let showPrivate = false;

  // How a network hands out addresses, in the words the form uses. Read from
  // what the daemon reports -- "no subnet means DHCP" was the old tell and
  // stopped being true when DHCP networks began recording their segment.
  /** The v6 segment, said next to the v4 one rather than instead of it. */
  const segment6 = (n: { subnet6?: string }) => n.subnet6 || '';
  const allocLabel = (n: { addressSource?: string; subnet?: string }) =>
    n.addressSource === 'dhcp'
      ? n.subnet ? `DHCP · ${n.subnet}` : 'DHCP'
      : n.addressSource === 'static'
        ? n.subnet ? `static · ${n.subnet}` : 'static'
        : n.subnet || '';

  // What one engine may be defaulted to: the built-ins it can take, then the
  // networks that list it. A default it cannot attach to is not an option,
  // which is the whole reason these are per engine.
  $: defaultChoices = (e: string) => {
    const out = [
      ...visibleBuiltIn.filter((b) => enginesFor(b).includes(e)).map((b) => b.name),
      ...networks.filter((n) => !n.problem && (n.engines ?? []).includes(e)).map((n) => n.name),
    ];
    // Whatever is stored stays listed even when it no longer qualifies -- a
    // select whose value matches no option renders the first one instead, and
    // then the page is showing a default that is not the one in the file.
    const cur = defaultFor[e];
    if (cur && !out.includes(cur)) out.push(cur);
    return out;
  };

  // One engine, one value. No cross-row state to reconcile and no way to
  // express two defaults for the same engine, which a per-row toggle could.
  async function setDefault(engine: string, name: string) {
    const was = defaultFor[engine] ?? '';
    defaultFor = { ...defaultFor, [engine]: name };
    const r = await fetch('/api/settings/network', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ network: name, engine }),
    });
    if (!r.ok) {
      defaultFor = { ...defaultFor, [engine]: was };
      toast((await r.text()).trim(), { kind: 'error' });
      return;
    }
    toast(`New installs on ${engine} will use ${name}`, { kind: 'success' });
  }

  // The user runs the commands in another window; nothing tells fjord when
  // they are done, so give them a way to say so without losing the dialog.
  // The host knows every network already defined and every address on every
  // interface; the operator inventing a range is being asked to remember all
  // of it. So ask the host.
  let suggesting = false;
  async function suggestSubnet() {
    suggesting = true;
    try {
      const q = kind?.engine ? '?engine=' + encodeURIComponent(forEngine || kind.engine) : '';
      const r = await fetch(`/api/networks/suggest${q}`);
      if (!r.ok) throw new Error((await r.text()).trim());
      form.subnet = (await r.json()).subnet ?? '';
      if (kind?.needsGateway) guessGateway();
    } catch (e) {
      toast(`${e}`, { kind: 'error' });
    } finally {
      suggesting = false;
    }
  }

  async function recheck() {
    rechecking = true;
    try {
      await refreshHost();
      if (parents.length) {
        showSetup = false;
        toast(`Found ${parents.length} ${parents.length === 1 ? 'bridge' : 'bridges'}`, { kind: 'success' });
      } else {
        toast('Still no bridge on this host', { kind: 'error' });
      }
    } finally {
      rechecking = false;
    }
  }
  $: if (kind) setups = (kind.parentSetups || []).map((x) => ({ ...x }));

  async function rerenderSetup(i: number, patch: Partial<Setup>) {
    const cur = { ...setups[i], ...patch };
    setups[i] = cur;
    setupBusy = cur.id;
    try {
      const q = new URLSearchParams({ kind: kind.id, nic: cur.interface || '', vlan: cur.vlan || '' });
      if (kind.engine) q.set('engine', kind.engine);
      const r = await fetch(`/api/networks/setup?${q}`);
      if (!r.ok) throw new Error((await r.text()).trim());
      setups[i] = await r.json();
      setups = setups;
    } catch (e) {
      toast(`${e}`, { kind: 'error' });
    } finally {
      setupBusy = '';
    }
  }

  // What this host can actually do, said once at the foot of the dialog. Built
  // from the same payload the form is built from, so it cannot drift from it
  // -- no engine is claimed "ready" on the strength of anything but the kinds
  // that engine just offered.
  $: engineSummary = [
    ...kinds
      .reduce((by, k) => {
        for (const e of k.engines?.length ? k.engines : k.engine ? [k.engine] : []) {
          by.set(e, [...(by.get(e) ?? []), k.label]);
        }
        return by;
      }, new Map<string, string[]>())
      .entries(),
  ].map(([engine, labels]) => `${engine} can create ${labels.join(' and ')} networks here`);

  $: kind = kinds.find((k) => k.id === kindID) || kinds[0];

  // Which engine will own the network. Only asked for a kind whose result
  // belongs to the engine rather than to the host: podman's private network is
  // a CNI bridge and appjail's is a virtualnet, and neither can attach to the
  // other's -- so the answer cannot be inferred from the default engine, which
  // is what it used to fall out of.
  // Shared kinds may be for either engine ("" = both); an engine-owned kind
  // must name one.
  $: engineOptions = kind?.shared ? [...allEngines, ''] : (kind?.engines ?? []);
  $: if (kind) {
    forEngine = engineOptions.includes(forEngine) ? forEngine : kind.shared ? '' : (kind.engine ?? '');
  }
  // Keep it meaningful for whatever kind is selected. It is seeded from
  // kinds[0] before the user has chosen anything, so a kind with no DHCP was
  // inheriting "dhcp" from the LAN kind next to it -- which then decided both
  // what the form said and what the request carried.
  $: if (kind && !kind.supportsDhcp && addressSource === 'dhcp') addressSource = 'pool';
  $: needsParent = !!kind?.parentLabel;
  $: isPrivate = kind?.id === 'nat';
  // A LAN network has nowhere to attach without a bridge; say so rather than
  // letting someone fill in a form that cannot succeed.
  $: blocked = needsParent && parents.length === 0;
  // How many networks already hang off a bridge. A bridge is a wire, and a
  // wire carries as many networks as you like -- one taking DHCP leases and
  // one for addresses you assign by hand is an ordinary pair. So this is a
  // count to show, not a reason to stop.
  $: netsOn = (bridge: string) => networks.filter((n) => n.bridge === bridge).length;

  async function openCreate() {
    form = { name: '', parent: '', subnet: '', gateway: '', subnet6: '', gateway6: '', mtu: '', rangeStart: '', rangeEnd: '', description: '' };
    filled = { subnet: '', gateway: '' };
    forEngine = '';
    advanced = false;
    createError = '';
    creating = true;
    await refreshHost();
    kindID = kinds[0]?.id || '';
    // One allocator beats two on the same wire, so DHCP leads where the
    // plugin can do it. A pool is the fallback, not the default.
    addressSource = kinds[0]?.supportsDhcp ? 'dhcp' : 'pool';
    // Offered when there is no bridge at all. It used to open whenever every
    // bridge already had a network, which told an operator to build a second
    // wire when all they wanted was a second network on the first one.
    showSetup = parents.length === 0;
  }

  // Offer the usual .1 for a /24 so the common case is one less field to fill.
  function guessGateway() {
    const m = form.subnet.match(/^(\d+\.\d+\.\d+)\.\d+\/\d+$/);
    if (m && !form.gateway) form.gateway = m[1] + '.1';
  }

  $: chosenParent = parents.find((p) => p.name === form.parent);

  // What applyParent last put in these fields. A segment is a fact about the
  // PARENT, so it has to follow the parent -- but only the value we filled in
  // may be replaced, never one the operator typed.
  let filled = { subnet: '', gateway: '' };

  // Picking a parent fills in what the host already knows: the segment's
  // subnet and gateway, a name following the bridge, and a default range.
  // A sentinel option in the dropdown: choosing it opens the setup help
  // rather than selecting a parent. The dropdown is where someone looks when
  // it does not list what they need, so the way out belongs there.
  const NEW_PARENT = '__new__';

  function onParentChange() {
    if (form.parent === NEW_PARENT) {
      form.parent = '';
      showSetup = true;
      return;
    }
    applyParent();
  }

  function applyParent() {
    const p = chosenParent;
    if (!p) return;
    if (!form.name.trim()) {
      const base = p.name.replace(/bridge$/, '') || p.name;
      let n = base;
      for (let i = 2; networks.some((x) => x.name === n); i++) n = base + i;
      form.name = n;
    }
    // The segment is filled in whichever way addresses are allocated. On a
    // range it is the pool; on DHCP it is only a record of what the wire is --
    // and that record is what lets a stack be pinned to a fixed address there
    // later, so leaving it blank is not "not needed", it is a capability
    // quietly dropped. defaultRange stays pool-only: a range is a pool.
    // Replaced, not just filled: picking a second bridge used to leave the
    // first one's segment sitting there. On DHCP that field lives under
    // Advanced, so the stale value was invisible -- and it went into the
    // conflist as the segment of a bridge it had nothing to do with. A parent
    // the host cannot see a segment for clears it back to empty, which is the
    // honest answer and the one the form then asks about.
    if (form.subnet.trim() === filled.subnet) {
      form.subnet = p.subnet ?? '';
      filled.subnet = form.subnet;
    }
    if (form.gateway.trim() === filled.gateway) {
      form.gateway = p.gateway ?? '';
      filled.gateway = form.gateway;
    }
    defaultRange();
  }

  // A blank range means host-local may hand out the WHOLE subnet, on a segment
  // where a DHCP server is usually leasing from the same pool -- a duplicate
  // address that surfaces days later. Default to a slice near the top instead,
  // so the safe choice is the one you get by doing nothing.
  function defaultRange() {
    if (addressSource !== 'pool' || !kind?.supportsRange) return;
    if (form.rangeStart.trim() || form.rangeEnd.trim()) return;
    const m = form.subnet.trim().match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)\/(\d+)$/);
    if (!m) return;
    const prefix = +m[5];
    const size = 2 ** (32 - prefix);
    if (size < 64) return; // too small to carve a range out of
    const base = ((+m[1] << 24) >>> 0) + (+m[2] << 16) + (+m[3] << 8) + +m[4];
    const end = base + size - 1 - 5; // stay clear of the broadcast address
    const start = end - 50;
    const fmt = (n: number) => [24, 16, 8, 0].map((sh) => (n >>> sh) & 255).join('.');
    form.rangeStart = fmt(start);
    form.rangeEnd = fmt(end);
  }

  $: nameTaken = !!form.name.trim() && networks.some((n) => n.name === form.name.trim());
  // DHCP is the one source that needs no segment from us -- the server on the
  // wire supplies it. Everything else allocates, and cannot without one.
  $: needsSubnet = isPrivate || addressSource !== 'dhcp';
  $: canSubmit = form.name.trim() && !nameTaken && (!needsParent || form.parent) &&
    (!needsSubnet || !!form.subnet.trim());
  // ...and a required field cannot hide. Subnet lives in Advanced, collapsed
  // by default, so picking Static left Create disabled with the one field that
  // would enable it out of sight and no reason on screen.
  $: if (needsSubnet && !form.subnet.trim()) advanced = true;
  // Said on the button too, for the moment before Advanced is noticed.
  $: createBlockedBy = !form.name.trim()
    ? 'Name this network'
    : nameTaken
      ? `A network named ${form.name.trim()} already exists`
      : needsParent && !form.parent
        ? 'Pick a bridge'
        : needsSubnet && !form.subnet.trim()
          ? 'This network allocates its own addresses -- give it a subnet'
          : '';

  async function submitCreate() {
    if (!canSubmit) return;
    submitting = true;
    createError = '';
    const body: any = { name: form.name.trim(), kind: kind?.id, addressSource };
    if (forEngine) body.for = forEngine;
    // "both" (empty) has no owner to name: either engine writes the same
    // conflist, so the request goes to whichever offered the kind.
    const owner = forEngine || kind?.engine || '';
    const eng = owner ? '?engine=' + encodeURIComponent(owner) : '';
    if (needsParent) body.parent = form.parent;
    if (isPrivate || addressSource === 'pool' || addressSource === 'static') {
      body.subnet = form.subnet.trim();
      if (kind?.needsGateway) body.gateway = form.gateway.trim();
    } else if (addressSource === 'dhcp') {
      // Record the segment even though DHCP allocates on it. Nothing here
      // allocates from it -- it is what lets a stack be given a fixed address
      // later, since appjail configures the interface itself and needs the
      // prefix length. Declining to write it down is what made a fixed
      // address work on a range network and not on this one, on the same
      // wire, by the same mechanism.
      if (form.subnet.trim()) body.subnet = form.subnet.trim();
      if (form.gateway.trim()) body.gateway = form.gateway.trim();
    }
    // The v6 half is independent of the v4 one and belongs only to a network
    // that allocates: a DHCP network's addresses come from the CNI dhcp
    // plugin, which is IPv4-only, so its v6 would have to be SLAAC and the
    // epair plugin does not do that. The daemon refuses it; the form does not
    // offer it.
    if (addressSource === 'pool' || addressSource === 'static') {
      if (form.subnet6.trim()) body.subnet6 = form.subnet6.trim();
      if (form.gateway6.trim()) body.gateway6 = form.gateway6.trim();
    }
    if (kind?.supportsMtu && form.mtu.trim()) body.mtu = parseInt(form.mtu, 10);
    if (addressSource === 'pool' && form.rangeStart.trim()) body.rangeStart = form.rangeStart.trim();
    if (addressSource === 'pool' && form.rangeEnd.trim()) body.rangeEnd = form.rangeEnd.trim();
    if (kind?.supportsDescription && form.description.trim()) body.description = form.description.trim();
    try {
      const res = await fetch('/api/networks' + eng, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error(await res.text());
      creating = false;
      toast(`Network ${body.name} created`, { kind: 'success' });
      await load();
    } catch (e: any) {
      createError = e.message || 'Create failed';
    } finally {
      submitting = false;
    }
  }

  // ---- delete (two-click confirm) ----
  // A network with attachments is never deletable from here, and a 409 is NOT
  // escalated into a force button: that would put a destructive action under
  // the cursor exactly where the user just clicked to confirm. Deleting a
  // network out from under running containers strands them on an address
  // nothing can route or clean up. Force remains on the API for stale state.
  let confirmDelete = '';
  /** The network whose IPv6 panel is open, '' for none. */
  let editing6 = '';
  let edit6 = { subnet6: '', gateway6: '' };

  /** Only a pool or static network can carry an IPv6 segment: a DHCP one's
   *  addresses come from the CNI dhcp plugin, which is IPv4-only. */
  const canHaveV6 = (n: Network) =>
    !isPrivateNet(n) && (n.addressSource === 'pool' || n.addressSource === 'static');

  function openEdit6(n: Network) {
    editing6 = n.name;
    edit6 = { subnet6: n.subnet6 ?? '', gateway6: n.gateway6 ?? '' };
  }

  async function saveSegment6(name: string, remove = false) {
    try {
      const res = await fetch('/api/networks/' + encodeURIComponent(name), {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(
          remove ? { subnet6: '' } : { subnet6: edit6.subnet6.trim(), gateway6: edit6.gateway6.trim() },
        ),
      });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      editing6 = '';
      toast(remove ? 'IPv6 segment removed' : 'IPv6 segment saved — stacks pick it up when they next start', { kind: 'success' });
      await load();
    } catch (e: any) {
      toast(e.message || 'Could not change the IPv6 segment', { kind: 'error' });
    }
  }
  async function del(name: string) {
    try {
      const res = await fetch(`/api/networks/${encodeURIComponent(name)}?engine=${encodeURIComponent(engineOf(name))}`, { method: 'DELETE' });
      if (!res.ok) throw new Error(await res.text());
      confirmDelete = '';
      await load();
    } catch (e: any) {
      toast(e.message || 'Delete failed', { kind: 'error' });
    }
  }

  const inputCls =
    'bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent';
</script>

<div class="h-full flex flex-col">
  <div class="flex items-center justify-between mb-4 shrink-0">
    <div>
      <h2 class="text-2xl font-bold text-fjord-fg">Networks</h2>
      <p class="text-sm text-fjord-fg-dim">
        Give a stack its own address instead of publishing ports on the host. Attach one when
        installing, or from a stack's <b class="text-fjord-fg-muted">Resources</b> tab.
      </p>
    </div>
    <div class="flex items-center gap-2 shrink-0">
    {#if kinds.length > 0}
      <button
        on:click={openCreate}
        class="flex items-center gap-2 bg-fjord-accent hover:bg-fjord-accent-hover text-white font-medium py-2 px-4 rounded-lg text-sm"
        ><Icon name="plus" size={14} /> New Network…</button
      >
    {/if}
    </div>
  </div>

  <div class="flex-1 overflow-y-auto">
    {#if loading}
      <div class="flex items-center gap-3 text-fjord-fg-dim text-sm"><Spinner size={18} /> Loading…</div>
    {:else if error}
      <EmptyState icon="alert" title="Networks Unavailable" description={error} />
    {:else}
      <!-- One dropdown per engine, above the list rather than a toggle on each
           row. A row-level control cannot say "one per engine" -- nothing
           stops two rows claiming the same engine -- and it made the reader
           scan every row to answer what an install will actually pick. -->
      {#if allEngines.length}
        <div class="border border-fjord-border rounded-xl px-4 py-3 mb-4">
          <div class="text-xs text-fjord-fg-dim mb-2">New installs use</div>
          <div class="flex flex-wrap gap-x-6 gap-y-2">
            {#each allEngines as e}
              <label class="flex items-center gap-2">
                {#if allEngines.length > 1}
                  <span class="text-xs font-mono text-fjord-fg-secondary w-16 shrink-0">{e}</span>
                {/if}
                <select
                  value={defaultFor[e] ?? 'bridge'}
                  on:change={(ev) => setDefault(e, (ev.currentTarget as HTMLSelectElement).value)}
                  class="bg-fjord-inset border border-fjord-border rounded px-2 py-1 text-sm font-mono text-fjord-fg-body focus:border-fjord-accent outline-none"
                >
                  {#each defaultChoices(e) as name}
                    <option value={name}>{name}</option>
                  {/each}
                </select>
              </label>
            {/each}
          </div>
        </div>
      {/if}
      <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border">
        <!-- The built-ins. Neither is a network anyone creates, shares or
             deletes: they are the states a stack is in without one, named as
             podman names them so the page and `podman inspect` agree. -->
        {#snippet engines(list: string[])}
          <!-- One column, one treatment, one place. The engine used to be an
               amber badge on the left for a built-in and plain text on the
               right for a network -- the same fact in two places, looking like
               two different kinds of thing. -->
          <div
            class="shrink-0 w-28 text-right text-xs text-fjord-fg-dim font-mono truncate"
            title="Usable by: {list.join(', ')}"
          >{list.join(' · ')}</div>
        {/snippet}
        {#each visibleBuiltIn as b}
          <div class="flex items-center gap-3 px-4 py-3">
            <div class="shrink-0 text-fjord-fg-faint"><Icon name="globe" size={18} /></div>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="font-mono text-sm text-fjord-fg-secondary truncate">{b.name}</span>
                <span class="text-[11px] font-medium bg-fjord-border text-fjord-fg-muted px-1.5 py-0.5 rounded">built in</span>
              </div>
              <div class="text-xs text-fjord-fg-dim truncate">{b.detail}</div>
            </div>
            {#if showEngines}{@render engines(enginesFor(b))}{/if}
            <!-- Same slot and same word as a real network's action, greyed:
                 a bare em dash in the Delete column read as a fourth kind of
                 thing rather than as "this one cannot be deleted". -->
            <span
              class="shrink-0 text-xs px-2 py-1 text-fjord-fg-faint cursor-not-allowed"
              title="Built in — always available, nothing to delete">Delete</span
            >
          </div>
        {/each}
        {#if lanNets.length === 0 && privateNets.length === 0}
          <!-- The built-ins stay above: they are always real states a stack can be
               in, and always a value the default can take. Replacing the whole
               list with an empty state took it away exactly when someone had
               deleted everything and most needed to see where things stand. -->
          <div class="px-4 py-3 text-sm text-fjord-fg-dim">
            {kinds.length === 0
              ? kindsNote || 'This engine cannot create networks on this host.'
              : 'No networks yet — create one to give stacks an address of their own.'}
          </div>
        {/if}
        {#snippet netRow(n: Network)}
          <div class="flex items-center gap-3 px-4 py-3">
            <div class="shrink-0 text-fjord-fg-muted"><Icon name="globe" size={18} /></div>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="font-mono text-sm text-fjord-fg truncate">{n.name}</span>
                <span class="text-[11px] font-medium bg-fjord-accent/20 text-fjord-accent px-1.5 py-0.5 rounded">{n.driver}</span>
              </div>
              <div class="text-xs text-fjord-fg-dim font-mono truncate">
                {allocLabel(n)}{n.gateway && n.addressSource !== 'dhcp' ? ` · gw ${n.gateway}` : ''}{segment6(n)
                  ? ` · ${segment6(n)}${n.gateway6 ? ` gw ${n.gateway6}` : ''}`
                  : ''}
              </div>
              <!-- IPv6 is stated whether or not the network has it. Showing
                   the line only when a segment exists made the absence
                   indistinguishable from fjord not doing IPv6 at all, and the
                   only way to add one was to delete the network every stack
                   was on. -->
              {#if !canHaveV6(n) && !isPrivateNet(n) && n.addressSource === 'dhcp'}
                <!-- Stated, not omitted. A DHCP network showed no IPv6 line at
                     all, which is the same silent absence this block exists to
                     end: the answer "it cannot" is information, and without it
                     the only way to find out is to ask. -->
                <div class="text-xs text-fjord-fg-faint">
                  IPv6 — not on a DHCP network{#if n.bridge} · a pool network on {n.bridge} can carry one{/if}
                </div>
              {/if}
              {#if canHaveV6(n)}
                {#if editing6 === n.name}
                  <div class="mt-1.5 p-2 rounded-lg bg-fjord-inset/40 border border-fjord-border">
                    <div class="flex flex-wrap items-end gap-2">
                      <div class="flex flex-col gap-1">
                        <label class="text-[10px] font-semibold text-fjord-fg-muted" for="e6-{n.name}">IPv6 subnet</label>
                        <input id="e6-{n.name}" bind:value={edit6.subnet6} placeholder="fd00:4:103::/64"
                          class="w-48 bg-fjord-inset border border-fjord-border rounded-lg px-2 py-1 text-xs font-mono text-fjord-fg-body" />
                      </div>
                      <div class="flex flex-col gap-1">
                        <label class="text-[10px] font-semibold text-fjord-fg-muted" for="e6gw-{n.name}">IPv6 gateway</label>
                        <input id="e6gw-{n.name}" bind:value={edit6.gateway6} placeholder="fd00:4:103::1"
                          class="w-44 bg-fjord-inset border border-fjord-border rounded-lg px-2 py-1 text-xs font-mono text-fjord-fg-body" />
                      </div>
                      <button on:click={() => saveSegment6(n.name)} disabled={!edit6.subnet6.trim()}
                        class="px-2.5 py-1 rounded-lg text-xs font-medium bg-fjord-border hover:bg-fjord-accent hover:text-white transition-colors disabled:opacity-40">Save</button>
                      {#if n.subnet6}
                        <button on:click={() => saveSegment6(n.name, true)}
                          class="px-2 py-1 rounded-lg text-xs text-fjord-fg-muted hover:text-fjord-danger">Remove</button>
                      {/if}
                      <button on:click={() => (editing6 = '')}
                        class="px-2 py-1 rounded-lg text-xs text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
                    </div>
                    {#if n.usedBy?.length}
                      <p class="text-[11px] text-fjord-fg-faint mt-1.5">
                        {n.usedBy.length} attached — they keep their current addresses until each is restarted.
                      </p>
                    {/if}
                  </div>
                {:else}
                  <div class="text-xs text-fjord-fg-faint">
                    IPv6 {n.subnet6 ? '' : '— none'}
                    <button on:click={() => openEdit6(n)}
                      class="ml-1 text-fjord-accent hover:underline">{n.subnet6 ? 'change' : '+ add'}</button>
                  </div>
                {/if}
              {/if}
              {#if isPrivateNet(n)}
                <!-- Whose it is, on the row itself: the disclosure above can
                     only give a count, and "made by a stack" over all of them
                     was a lie about the engine's own default. -->
                <div class="text-xs text-fjord-fg-faint">{privateWhose(n)}</div>
              {/if}
              {#if n.problem}
                <div class="text-xs text-fjord-warning mt-0.5">{n.problem}</div>
              {/if}
            </div>
            {#if showEngines}{@render engines(n.engines ?? [])}{/if}
            {#if n.usedBy?.length}
              <div class="flex items-center gap-1 shrink-0 max-w-[35%] overflow-hidden" title="Attached: {n.usedBy.join(', ')}">
                {#each n.usedBy.slice(0, 3) as c}
                  <span class="text-[10px] px-1.5 py-0.5 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-muted">{c}</span>
                {/each}
                {#if n.usedBy.length > 3}
                  <span class="text-[10px] text-fjord-fg-dim">+{n.usedBy.length - 3}</span>
                {/if}
              </div>
            {/if}
            {#if !canRemove}
              <!-- nothing: this engine does not own these networks -->
            {:else if engineOwned(n)}
              <!-- The engine's own default (appjail's ajnet, podman's podman).
                   It is not fjord's to delete: the engine recreates it, and
                   "bridge" -- which is how a stack asks for exactly this
                   network -- is broken until it does. Nothing attached right
                   now made it look like a safe thing to tidy away. -->
              <span
                class="text-xs px-2 py-1 text-fjord-fg-faint cursor-not-allowed"
                title="The engine's own default network — it makes this one itself, and the bridge mode needs it"
                >Delete</span
              >
            {:else if n.usedBy?.length}
              <span
                class="text-xs px-2 py-1 text-fjord-fg-faint cursor-not-allowed"
                title="In use by {n.usedBy.join(', ')} — detach those first">Delete</span
              >
            {:else if confirmDelete === n.name}
              <button on:click={() => del(n.name)} class="text-xs px-2 py-1 rounded bg-fjord-danger hover:bg-fjord-danger-hover text-white">Confirm Delete</button>
              <button on:click={() => (confirmDelete = '')} class="text-xs px-2 py-1 rounded text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
            {:else}
              <button
                on:click={() => (confirmDelete = n.name)}
                class="text-xs px-2 py-1 rounded text-fjord-fg-muted hover:text-fjord-danger hover:bg-fjord-border transition-colors"
                >Delete</button
              >
            {/if}
          </div>
        {/snippet}
        {#each lanNets as n}{@render netRow(n)}{/each}
        {#if privateNets.length}
          <button
            on:click={() => (showPrivate = !showPrivate)}
            class="w-full flex items-center gap-2 px-4 py-2.5 text-left text-xs text-fjord-fg-muted hover:text-fjord-fg hover:bg-fjord-inset/40 transition-colors"
          >
            <span class="w-3">{showPrivate ? '▼' : '▶'}</span>
            <span
              >{privateNets.length} {privateNets.length === 1 ? 'network' : 'networks'} not reachable
              from your LAN</span
            >
            <span class="text-fjord-fg-faint">— {privateSummary}</span>
          </button>
          {#if showPrivate}
            {#each privateNets as n}{@render netRow(n)}{/each}
          {/if}
        {/if}
      </div>
    {/if}
  </div>
</div>

{#if creating}
  <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
  <div
    class="fixed inset-0 bg-black/70 backdrop-blur-sm flex items-center justify-center z-50 p-4"
    on:click|self={() => (creating = false)}
  >
    <div class="bg-fjord-card border border-fjord-border rounded-xl shadow-2xl w-full max-w-lg p-6">
      <h3 class="text-lg font-bold text-fjord-fg mb-4">New Network</h3>

      <div class="flex flex-col gap-4">
        {#if kinds.length > 1}
          <div class="flex gap-2">
            {#each kinds as k}
              <button
                on:click={() => (kindID = k.id)}
                class="flex-1 flex items-center justify-center gap-2 px-3 py-2 rounded-md text-sm font-medium border transition-colors {kind?.id ===
                k.id
                  ? 'bg-fjord-accent/20 text-fjord-accent border-fjord-accent/50'
                  : 'border-fjord-border text-fjord-fg-secondary hover:bg-fjord-border'}">{k.label}</button
              >
            {/each}
          </div>
        {/if}
        <!-- Only when there is a real choice. With one engine enabled, a shared
             kind still offered "podman" and "both", which are the same thing
             said twice; and a kind only one engine can make offered that one
             engine. Either way the answer is already decided, so asking is
             noise. Left unset, which keeps the network usable by whatever gets
             enabled later rather than stamping it for whoever happens to be on
             right now. -->
        {#if allEngines.length > 1 && engineOptions.length > 1}
          <div class="flex flex-col gap-1">
            <span class="text-sm font-semibold text-fjord-fg-secondary">For</span>
            <div class="flex gap-2">
              {#each engineOptions as e}
                <button
                  type="button"
                  on:click={() => (forEngine = e)}
                  class="flex-1 px-3 py-2 rounded-md text-sm font-medium border transition-colors {forEngine === e
                    ? 'bg-fjord-accent/20 text-fjord-accent border-fjord-accent/50'
                    : 'border-fjord-border text-fjord-fg-secondary hover:bg-fjord-border'}">{e || 'both'}</button
                >
              {/each}
            </div>
            <p class="text-xs text-fjord-fg-faint">
              {#if !kind?.shared}
                Only <span class="font-mono">{forEngine}</span> stacks can attach to it — each engine makes its own
                kind of private network, and neither can join the other's.
              {:else if forEngine === 'appjail'}
                One bridge, one segment — this only narrows the form to what appjail can do.
              {:else if forEngine}
                One bridge, one segment — this only narrows the form to what {forEngine} can do.
              {:else}
                Either engine can attach to it.
              {/if}
            </p>
          </div>
        {/if}
        {#if kindsNote}
          <!-- Why a kind you might expect is not on this host. Shown whenever
               there is one: the kinds that ARE available do not make it any
               less true that another is missing. -->
          <p class="text-xs text-fjord-warning bg-fjord-warning/10 border border-fjord-warning/20 rounded-md px-3 py-2">
            {kindsNote}
          </p>
        {/if}
        {#if kind?.help}
          <p class="text-xs text-fjord-fg-dim -mt-2">{kind.help}</p>
        {/if}
        <!-- Where the addresses come from is half the decision, and it is the
             half people get wrong. The payload carried it and the form never
             said it. -->

        {#snippet setupCard(ps: Setup, i: number)}
          <div class="flex flex-col gap-2">
            <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
              {#if ps.inputs?.includes('interface') && kind.parentInterfaces?.length}
                <label class="flex items-center gap-1.5 text-xs text-fjord-fg-dim">
                  Interface
                  <select
                    value={ps.interface}
                    on:change={(e) => rerenderSetup(i, { interface: e.currentTarget.value })}
                    class="text-xs bg-fjord-bg border border-fjord-border rounded px-1.5 py-0.5 text-fjord-fg">
                    {#each kind.parentInterfaces as n}
                      <option value={n.name}>{n.name}{n.detail ? ` — ${n.detail}` : ''}</option>
                    {/each}
                  </select>
                </label>
              {/if}
              {#if ps.inputs?.includes('vlan')}
                <!-- Tagged or not is the only difference between the two ways
                     to do this, so it is a toggle rather than a second card. -->
                <label class="flex items-center gap-1.5 text-xs text-fjord-fg-dim">
                  <input
                    type="checkbox" checked={!!ps.vlan}
                    on:change={(e) => rerenderSetup(i, { vlan: e.currentTarget.checked ? 'auto' : '' })}
                    class="accent-fjord-accent" />
                  Tagged VLAN
                </label>
                {#if ps.vlan}
                  <label class="flex items-center gap-1.5 text-xs text-fjord-fg-dim">
                    id
                    <input
                      type="number" min="1" max="4094" value={ps.vlan}
                      on:change={(e) => rerenderSetup(i, { vlan: e.currentTarget.value })}
                      class="w-16 text-xs bg-fjord-bg border border-fjord-border rounded px-1.5 py-0.5 text-fjord-fg" />
                  </label>
                {/if}
              {/if}
              {#if setupBusy === ps.id}<Spinner size={12} />{/if}
            </div>
            <FixSnippet fix={ps.snippet} />
            {#if ps.note}<span class="text-xs text-fjord-fg-dim">{ps.note}</span>{/if}
          </div>
        {/snippet}

        {#if blocked}
          <div class="flex flex-col gap-2">
            {#if kind.parentSetups?.length}
              <p class="text-sm text-fjord-fg-secondary">
                No {kind.parentLabel?.toLowerCase()} on this host yet. A {kind.parentLabel?.toLowerCase()} is
                persistent host configuration, so fjord does not create one — here is what to add:
              </p>
              {#each setups as ps, i}{@render setupCard(ps, i)}{/each}
              <div class="flex items-center gap-2">
                <button
                  type="button"
                  on:click={recheck}
                  disabled={rechecking}
                  class="flex items-center gap-1.5 text-sm px-3 py-1.5 rounded-lg bg-fjord-accent hover:bg-fjord-accent-hover text-white disabled:opacity-50">
                  {#if rechecking}<Spinner size={12} />{/if}
                  Check again
                </button>
                <span class="text-xs text-fjord-fg-dim">once you have run it on the host</span>
              </div>
            {:else}
              <p class="text-sm text-fjord-fg-secondary">
                No {kind.parentLabel?.toLowerCase()} on this host yet. A {kind.parentLabel?.toLowerCase()} is
                persistent host configuration, so fjord does not create one — make one on the host, then
                reopen this dialog.
              </p>
            {/if}
          </div>
        {:else}
          <div class="flex flex-col gap-1">
            <label class="text-sm font-semibold text-fjord-fg-secondary" for="n-name">Name</label>
            <input id="n-name" bind:value={form.name} placeholder="vlan4, lan, …" class={inputCls} />
          </div>

          {#if isPrivate}
            <div class="flex flex-col gap-1">
              <label class="text-sm font-semibold text-fjord-fg-secondary" for="n-priv">Subnet</label>
              <div class="flex gap-2">
                <input id="n-priv" bind:value={form.subnet} placeholder="10.100.0.0/24" class="{inputCls} flex-1" />
                <!-- Fills a blank field only, the same rule the MAC field
                     follows: overwriting a range someone typed has to be
                     deliberate, so clear it first. -->
                <button
                  type="button"
                  on:click={suggestSubnet}
                  disabled={suggesting || !!form.subnet.trim()}
                  title={form.subnet.trim()
                    ? 'Clear the field first'
                    : 'Pick a private range nothing on this host uses'}
                  class="shrink-0 flex items-center gap-1.5 px-3 rounded-md border border-fjord-border text-sm text-fjord-fg-secondary hover:bg-fjord-border disabled:opacity-30 disabled:hover:bg-transparent"
                >
                  {#if suggesting}<Spinner size={12} />{/if}
                  Generate
                </button>
              </div>
              <span class="text-xs text-fjord-fg-dim">
                A range nothing else uses — appjail creates the bridge and takes the first address as
                the gateway.
              </span>
            </div>
          {/if}

          {#if needsParent}
            <div class="flex flex-col gap-1">
              <div class="flex items-baseline justify-between gap-3">
                <label class="text-sm font-semibold text-fjord-fg-secondary" for="n-parent">{kind.parentLabel}</label>
                {#if kind.parentSetups?.length && !showSetup}
                  <!-- The dropdown's sentinel is only found by someone who opens
                       the dropdown, which nobody does when it already lists
                       something. This stays visible. -->
                  <button
                    type="button"
                    on:click={() => (showSetup = true)}
                    class="shrink-0 text-xs text-fjord-accent hover:underline"
                    >Set up another {kind.parentLabel?.toLowerCase()}</button
                  >
                {/if}
              </div>
              <select id="n-parent" bind:value={form.parent} on:change={onParentChange} class={inputCls}>
                <option value="">Select a {kind.parentLabel?.toLowerCase()}…</option>
                {#each parents as p}
                  <option value={p.name}
                    >{p.name}{netsOn(p.name)
                      ? ` (${netsOn(p.name)} network${netsOn(p.name) > 1 ? 's' : ''})`
                      : ''}</option
                  >
                {/each}
                {#if kind.parentSetups?.length}
                  <option value={NEW_PARENT}>+ Set up a new {kind.parentLabel?.toLowerCase()}…</option>
                {/if}
              </select>
              <!-- What the host already knows about the segment behind this
                   bridge. It was being read into the form's subnet/gateway
                   fields and then hidden under Advanced, so the one moment it
                   answers a question -- "is this the right bridge?" -- it was
                   nowhere on screen. -->
              {#if chosenParent}
                <p class="text-xs text-fjord-fg-dim mt-1.5 flex flex-wrap gap-x-3 gap-y-0.5 font-mono">
                  {#if chosenParent.subnet}<span>{chosenParent.subnet}</span>{/if}
                  {#if chosenParent.gateway}<span>gateway {chosenParent.gateway}</span>{/if}
                  {#if chosenParent.hostIp}<span>host {chosenParent.hostIp}</span>{/if}
                  {#if !chosenParent.subnet && !chosenParent.gateway && !chosenParent.hostIp}
                    <span>no address on this host yet</span>
                  {/if}
                </p>
              {/if}
              {#if showSetup && kind.parentSetups?.length}
                <div class="mt-2 flex flex-col gap-3 border-l-2 border-fjord-border pl-3">
                  <div class="flex items-start justify-between gap-3">
                    <p class="text-xs text-fjord-fg-dim">
                      A {kind.parentLabel?.toLowerCase()} is host configuration, so fjord does not create
                      one. Run this on the host, then check again.
                    </p>
                    <div class="shrink-0 flex items-center gap-3">
                      <button
                        type="button"
                        on:click={recheck}
                        disabled={rechecking}
                        class="flex items-center gap-1 text-xs text-fjord-accent hover:underline disabled:opacity-50">
                        {#if rechecking}<Spinner size={11} />{/if}
                        Check again
                      </button>
                      <button
                        type="button"
                        on:click={() => (showSetup = false)}
                        class="text-xs text-fjord-fg-muted hover:text-fjord-fg">Hide</button
                      >
                    </div>
                  </div>
                  {#each setups as ps, i}{@render setupCard(ps, i)}{/each}
                </div>
              {/if}
            </div>
          {/if}

          {#if kind?.supportsDhcp && !isPrivate}
            <div class="flex flex-col gap-1">
              <!-- Named for WHO supplies the address. "From DHCP" and "From a
                   range" named the mechanism and read as modes you were locked
                   into: one sounding like addresses were not yours to choose,
                   the other like it was how you chose one. Static is the third
                   and was missing -- it is what most people mean by "set an
                   IP", and it was reachable only by typing into a field on a
                   different screen. -->
              <span class="text-sm font-semibold text-fjord-fg-secondary">Addresses</span>
              <div class="flex gap-2">
                {#each [{ id: 'dhcp', label: 'DHCP' }, { id: 'pool', label: 'Range' }, { id: 'static', label: 'Static' }] as src}
                  <button
                    type="button"
                    on:click={() => { addressSource = src.id as typeof addressSource; applyParent(); }}
                    class="flex-1 px-3 py-2 rounded-md text-sm font-medium border transition-colors {addressSource ===
                    src.id
                      ? 'bg-fjord-accent/20 text-fjord-accent border-fjord-accent/50'
                      : 'border-fjord-border text-fjord-fg-secondary hover:bg-fjord-border'}">{src.label}</button
                  >
                {/each}
              </div>
              <p class="text-xs text-fjord-fg-dim">
                {#if addressSource === 'dhcp'}
                  The DHCP server on this segment leases one to each stack, keyed on its MAC, so reservations you
                  already have apply. A stack can still be given a fixed address instead.
                {:else if addressSource === 'pool'}
                  {form.subnet || 'The subnet'} is handed out by podman's IPAM from a range set aside here. appjail
                  cannot ask it, so an appjail stack needs an address typed in.
                {:else}
                  Nothing allocates: every stack is given an address when it attaches, on either engine. Forgetting
                  one is an error rather than a surprise.
                {/if}
              </p>
            </div>
          {/if}

          <button
            type="button"
            on:click={() => (advanced = !advanced)}
            class="flex items-center gap-1.5 text-xs text-fjord-fg-muted hover:text-fjord-fg self-start"
          >
            <Icon name={advanced ? 'chevron-up' : 'chevron-down'} size={12} /> Advanced
          </button>

          {#if !advanced && addressSource === 'dhcp' && !isPrivate}
            <p class="text-xs text-fjord-fg-dim -mt-1">
              Addresses come from the DHCP server on that segment, using each container's MAC — so
              your existing reservations apply and nothing here has to know the subnet.
            </p>
          {:else if !kind?.supportsDhcp && kind?.addressNote}
            <!-- The DHCP choice is simply absent for a kind that cannot have
                 it, which reads as something fjord forgot. Say whose addresses
                 these are instead. -->
            <p class="text-xs text-fjord-fg-dim -mt-1">{kind.addressNote}</p>
          {/if}

          {#if advanced}
            {#if (addressSource === 'pool' || addressSource === 'static') && !isPrivate}
              <div class="grid grid-cols-2 gap-2">
                <div class="flex flex-col gap-1">
                  <label class="text-xs font-semibold text-fjord-fg-muted" for="n-subnet"
                    >Subnet{#if addressSource === 'static'} (the segment){/if}</label
                  >
                  <input id="n-subnet" bind:value={form.subnet} on:blur={() => { guessGateway(); defaultRange(); }} placeholder="192.168.4.0/24" class={inputCls} />
                </div>
                {#if kind?.needsGateway}
                  <div class="flex flex-col gap-1">
                    <label class="text-xs font-semibold text-fjord-fg-muted" for="n-gw">Gateway</label>
                    <input id="n-gw" bind:value={form.gateway} placeholder="192.168.4.1" class={inputCls} />
                  </div>
                {/if}
              </div>

              <!-- IPv6, optional and independent: a network may be v4-only,
                   dual-stack, or v6-only. Offered only where something
                   allocates from a subnet, because that is the only place a
                   v6 range can come from. -->
              {#if addressSource === 'pool' || addressSource === 'static'}
                <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <div class="flex flex-col gap-1">
                    <label class="text-xs font-semibold text-fjord-fg-muted" for="n-subnet6">IPv6 subnet <span class="font-normal text-fjord-fg-faint">— optional</span></label>
                    <input id="n-subnet6" bind:value={form.subnet6} placeholder="fd00:4:103::/64" class={inputCls} />
                  </div>
                  {#if kind?.needsGateway}
                    <div class="flex flex-col gap-1">
                      <label class="text-xs font-semibold text-fjord-fg-muted" for="n-gw6">IPv6 gateway</label>
                      <input id="n-gw6" bind:value={form.gateway6} placeholder="fd00:4:103::1" class={inputCls} />
                    </div>
                  {/if}
                </div>
              {/if}

              <!-- A range belongs to the pool only: on a static network
                   nothing allocates, so there is nothing to allocate FROM.
                   Widening the block above to show the subnet for static
                   brought these along with it. -->
              {#if kind?.supportsRange && addressSource === 'pool'}
                <div class="grid grid-cols-2 gap-2">
                  <div class="flex flex-col gap-1">
                    <label class="text-xs font-semibold text-fjord-fg-muted" for="n-rs">Range start</label>
                    <input id="n-rs" bind:value={form.rangeStart} placeholder="192.168.4.200" class={inputCls} />
                  </div>
                  <div class="flex flex-col gap-1">
                    <label class="text-xs font-semibold text-fjord-fg-muted" for="n-re">Range end</label>
                    <input id="n-re" bind:value={form.rangeEnd} placeholder="192.168.4.250" class={inputCls} />
                  </div>
                </div>
                <p class="text-xs text-fjord-fg-dim -mt-2">
                  Containers get an address from this range — podman's IPAM picks one; a jail needs an
                  address you choose from it. Your router knows nothing about the range, so keep it clear
                  of whatever it leases.{#if chosenParent?.hostIp}
                    This host is <span class="font-mono text-fjord-fg-muted">{chosenParent.hostIp}</span> on
                    that segment.{/if}
                </p>
              {/if}
            {/if}

            {#if kind?.supportsMtu}
              <div class="flex flex-col gap-1 max-w-40">
                <label class="text-xs font-semibold text-fjord-fg-muted" for="n-mtu">MTU</label>
                <input id="n-mtu" bind:value={form.mtu} placeholder="1500" class={inputCls} />
              </div>
            {/if}
          {/if}
        {/if}

        {#if createError}
          <p class="text-sm text-fjord-danger">{createError}</p>
        {/if}

        {#if engineSummary.length}
          <div class="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-fjord-border pt-3 text-xs text-fjord-fg-faint">
            {#each engineSummary as e}
              <span class="flex items-center gap-1.5">
                <Icon name="check" size={12} class="text-fjord-success shrink-0" />
                <span>{e}</span>
              </span>
            {/each}
            {#if parents.length}
              <span class="font-mono"
                >{parents.length} {parents.length === 1 ? 'bridge' : 'bridges'} on this host</span
              >
            {/if}
          </div>
        {/if}

        <div class="flex justify-end gap-2 pt-2">
          <button on:click={() => (creating = false)} class="px-4 py-2 rounded-md text-sm text-fjord-fg-secondary hover:bg-fjord-border">Cancel</button>
          <button
            on:click={submitCreate}
            disabled={submitting || blocked || !canSubmit}
            title={createBlockedBy}
            class="px-4 py-2 rounded-md text-sm font-medium bg-fjord-accent hover:bg-fjord-accent-hover text-white disabled:opacity-50 disabled:cursor-not-allowed"
            >{submitting ? 'Creating…' : 'Create'}</button
          >
        </div>
      </div>
    </div>
  </div>
{/if}
