<script lang="ts">
  // A stack's services, each with the networks and storage IT holds -- and the
  // networking editable here rather than in a table below.
  //
  // The section this replaces described the STACK: for immich that meant
  // immich-server's two interfaces, while its database, redis and ML sat on
  // 10.99.0.2/.3/.4 and appeared nowhere on the page. Which service a row
  // belonged to was not written down anywhere.
  import { createEventDispatcher } from 'svelte';
  import Icon from './Icon.svelte';
  import AddMount from './AddMount.svelte';
  import { addressProblem, address6Problem, usableRange, randomMAC } from './network';
  import { setInterface, serviceType, switchType, isolatedServices, ALONE, type Iface } from './planSeed';

  type Net = {
    name: string;
    /** fjord made this for one stack; the daemon says so, because the signal
     *  differs per engine (appjail reports "engine", podman "pool"). */
    private?: boolean;
    /** The stack this segment was made for, named. Compared against
     *  stackName, so it does not matter which request fetched the list. */
    ownedBy?: string;
    driver?: string;
    subnet?: string;
    gateway?: string;
    /** The IPv6 half of the segment, when the network has one. Its presence is
     *  what decides whether a v6 address field is shown at all. */
    subnet6?: string;
    gateway6?: string;
    bridge?: string;
    addressSource?: string;
    problem?: string;
    /** The network's subnet is not the segment its bridge is on. */
    wireWarning?: string;
  };
  type Attachment = { network: string; ip?: string; ip6?: string; mac?: string; iface?: string };
  type ServiceVolume = {
    source?: string;
    dest?: string;
    name?: string;
    kind?: string;
    readOnly?: boolean;
  };
  type ServiceView = {
    name: string;
    container?: string;
    image?: string;
    state?: string;
    detail?: string;
    address?: string;
    hostNetwork?: boolean;
    networks?: Attachment[];
    /** What each network gave the running container. A pin is networks[].ip;
     *  this is only shown, never saved. */
    live?: Record<string, string>;
    volumes?: ServiceVolume[];
  };

  export let services: ServiceView[] = [];
  // Service name -> its pending update, from the stack's update check. Shown on
  // the row so a four-service stack says WHICH part is behind.
  export let updates: Record<string, { state: string; tag?: string; fromVersion?: string; toVersion?: string }> = {};
  // Service -> the image its last update replaced, when it can go back to it.
  export let rollbacks: Record<string, { ref: string; at: string }> = {};
  // Services pinned to an exact image (a rollback pins; so can the operator).
  export let pinned: Record<string, boolean> = {};
  let confirmRollback = '';
  const day = (iso: string) => (iso ? new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) : '');
  export let networks: Net[] = [];
  // AppJail: one interface per bridge per service, so a second network on a
  // bridge the service already uses is shown but not offered.
  export let oneBridgePerService = false;
  // AppJail: the built-in bridge only on its own. podman runs it next to other
  // networks; appjail has not been tried, and fjordd refuses the mix.
  export let bridgeAlone = false;
  /** service name -> its interfaces, staged. The parent owns it so Save can
   *  post it and the dirty check can see it. */
  export let edits: Record<string, Attachment[]> = {};
  /** Before install there is nothing running: no state, no image, no jail
   *  name, no volumes. Only the interfaces are real, so only they are shown. */
  export let planning = false;
  /** Built-ins this stack cannot take. An appjail director project cannot be
   *  put on the host's stack -- that is a jail parameter, not a director
   *  option -- so offering it produced a choice the install then refused. */
  export let unsupportedModes: string[] = [];
  /** Whose page this is. A private segment belonging to THIS stack is offered;
   *  every other stack's stays hidden. */
  export let stackName = '';
  /** Passed through to the per-service add form. */
  export let canUseVolumes = true;
  export let namedVolumes: { name: string; kind?: string }[] = [];
  export let folderSets: { id: string; name: string }[] = [];
  /** Host path the page-level picker filled in, bound back to the open form. */
  export let addSource = '';
  /** The service whose add form is open, '' for none. */
  export let addingTo = '';
  // Whether a service's version can be changed on its own. A director
  // stack's jails take their tag from the director file, which has no
  // per-service form.
  export let versionable = false;

  const dispatch = createEventDispatcher<{
    change: void;
    unmount: { service: string; dest: string };
    add: { service: string; kind: string; source: string; dest: string; readOnly: boolean };
    remote: { service: string; row: string; dest: string; readOnly: boolean };
    folderset: { service: string; id: string; dest: string; readOnly: boolean };
    browse: void;
    openAdd: string;
    closeAdd: void;
    openUpdate: void;
    rollback: string;
    unpin: string;
    version: { service: string; image: string };
  }>();
  // Which row is asking to be removed. Two clicks, in place -- the same shape
  // the stack-level list used, kept because unmounting is not undoable from
  // here and a stray click on a trash icon should not cost a mount.
  // Keyed "<service>:<dest>", not "<dest>": the same container path is mounted
  // by several services (/etc/localtime is on four of immich's), and a single
  // key put every one of those rows into confirm at the same click.
  let confirmUnmount = '';
  // The same two clicks for an interface. Keyed by row index, which shifts
  // under any edit -- so every edit (touch) drops a pending confirm rather
  // than leave it armed on whatever row slid into that slot.
  let confirmRemove = '';
  const touch = () => {
    confirmRemove = '';
    edits = { ...edits };
    dispatch('change');
  };

  let open: Record<string, boolean> = {};
  let opened = false;
  // Collapsed by default while planning: the summary line already says where
  // each part goes ("lan · private"), so four open rows is four times the
  // height to say what four lines said. On the stack page the first one opens,
  // since that page is where you go to change something.
  $: if (!opened && services.length) {
    opened = true;
    open = planning && services.length > 1 ? {} : { [services[0].name]: true };
  }
  const toggle = (n: string) => (open = { ...open, [n]: !open[n] });

  // Type is networks, host or none -- the same built-ins, in the same words,
  // as the Networks page. host and none hold no interface, so the table is
  // only for networks; bridge is one of them.
  const TYPES: { id: string; text: string }[] = [
    { id: 'networks', text: 'One or more interfaces, each on a network.' },
    { id: 'host', text: "No interface of its own — it binds this host's ports directly." },
    { id: 'none', text: 'No network at all: nothing in and nothing out.' },
  ];
  const isBuiltIn = (n: string) => n === 'bridge' || ALONE.includes(n);
  // Rows set aside by a trip to host or none, per service, until Save.
  let kept: Record<string, Iface[] | undefined> = {};
  function setType(svc: string, to: string) {
    const r = switchType(rows(svc), to, kept[svc], [{ network: 'bridge' }]);
    kept = { ...kept, [svc]: r.kept };
    edits[svc] = r.rows;
    touch();
  }
  // Each network is grouped under its own type, as the engine reports it
  // (epair here, macvlan on Linux); the built-in bridge, this stack's own
  // segment and bridge-driver networks go under bridge. The list is passed in,
  // not read here: the template only redraws for what it references, and the
  // networks arrive after the first draw.
  const bridgeKind = (n: Net) => n.driver === 'bridge' || !!n.private || n.addressSource === 'engine';
  function netGroups(nets: Net[], current: string): { label: string; nets: Net[] }[] {
    const list = pickable(nets, current);
    const out: { label: string; nets: Net[] }[] = [{ label: 'bridge', nets: list.filter(bridgeKind) }];
    for (const n of list.filter((x) => !bridgeKind(x))) {
      const label = n.driver || 'other';
      let g = out.find((x) => x.label === label);
      if (!g) out.push((g = { label, nets: [] }));
      g.nets.push(n);
    }
    return out;
  }
  function kindOf(nets: Net[], name: string): string {
    if (!name || name === 'bridge' || name === 'private') return 'bridge';
    const n = nets.find((x) => x.name === name);
    return n ? (bridgeKind(n) ? 'bridge' : n.driver || '') : '';
  }
  // A network the ENGINE allocates on is some stack's own private segment.
  // Joining another stack's is almost never the intent, and one per stack
  // makes this list grow by one for every multi-service app installed -- so
  // they are left out. "private" covers this stack's own. A row already on
  // one still offers it, or opening the page would silently lose it.
  const pickable = (nets: Net[], current: string) =>
    nets.filter(
      (n) =>
        isOwn(n) || !(n.private || n.addressSource === 'engine') || n.name === current,
    );
  const isOwn = (n: Net) => !!stackName && n.ownedBy === stackName;
  /**
   * Whether to offer "private" as a SPEC rather than by name.
   *
   * Before install there is no segment yet, so the spec is all there is. On
   * a stack that already has one, the segment itself is in the list (own),
   * and offering both put private in the picker twice under two different
   * names.
   */
  $: offerPrivateSpec = planning || !networks.some(isOwn);
  // Removing an interface is the edit that breaks a working stack quietly:
  // the row just has one fewer entry, and the app fails to start later. Named
  // here, on the service it happened to, while it is still being edited.
  $: isolated = new Set(isolatedServices(edits));
  const rows = (svc: string): Attachment[] => edits[svc] ?? [];
  const byName = (n: string) => networks.find((x) => x.name === n);
  // The network on another of this service's rows that uses n's bridge, or ''.
  function sameBridgeAs(svc: string, i: number, n: Net): string {
    if (!oneBridgePerService || !n.bridge) return '';
    return rows(svc).find((r, j) => j !== i && byName(r.network)?.bridge === n.bridge)?.network ?? '';
  }

  function addRow(svc: string) {
    const used = rows(svc).map((r) => r.network);
    // Prefer one this service is not on yet; failing that, anything at all --
    // an empty row is a control that looks broken, and naming the same network
    // twice is flagged on the row rather than prevented here.
    const unused = pickable(networks, '').find((n) => !used.includes(n.name) && !sameBridgeAs(svc, -1, n))?.name;
    const privateFree = planning && !used.includes('private') ? 'private' : '';
    const network = unused || privateFree || pickable(networks, '')[0]?.name || (planning ? 'private' : '');
    edits[svc] = [...rows(svc), { network, ip: '', mac: '' }];
    touch();
  }
  function removeRow(svc: string, i: number) {
    edits[svc] = rows(svc).filter((_, j) => j !== i);
    touch();
  }
  // Said in the confirm, not after: the interface that connects a service to
  // the rest of its stack looks like any other row, and taking it off is a
  // working stack turned into a broken one in one click.
  const cutsOff = (svc: string, i: number) =>
    !isolated.has(svc) &&
    isolatedServices({ ...edits, [svc]: rows(svc).filter((_, j) => j !== i) }).includes(svc);
  function setField(svc: string, i: number, field: 'network' | 'ip' | 'ip6' | 'mac', value: string) {
    edits[svc] = setInterface(rows(svc), i, field, value).rows;
    touch();
  }
  function genMAC(svc: string, i: number) {
    setField(svc, i, 'mac', randomMAC());
  }

  const DOT: Record<string, string> = {
    running: 'bg-fjord-success',
    stopped: 'bg-fjord-neutral',
    crashed: 'bg-fjord-danger',
  };
  const dot = (s?: string) => DOT[s ?? ''] ?? 'bg-fjord-warning';
  const mountLabel = (v: ServiceVolume) => v.source || v.name || '—';

  // The collapsed line has to answer "where is this one" on its own, or every
  // row has to be opened to read the page.
  function summary(s: ServiceView, r: Attachment[]): string {
    if (!r.length) return 'no network';
    return r.map((a) => {
      const ip = a.ip || s.live?.[a.network];
      return `${a.network || 'bridge'}${ip ? ' ' + ip : ''}`;
    }).join(' · ');
  }
  // A network named twice on one service is two names for one interface.
  function duplicate(r: Attachment[], i: number): boolean {
    return !!r[i]?.network && r.findIndex((x) => x.network === r[i].network) !== i;
  }
</script>

{#if !services.length}
  <p class="text-sm text-fjord-fg-dim">This stack reports no services.</p>
{:else}
  <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border">
    {#each services as s (s.name)}
      <div>
        <!-- Toggle and update chip side by side: the chip is its own button,
             and a button cannot sit inside the row's. -->
        <div class="flex items-center hover:bg-fjord-inset/50 transition-colors">
          <button on:click={() => toggle(s.name)} class="flex-1 min-w-0 flex items-center gap-3 pl-4 py-3 text-left">
            <span class="text-fjord-fg-dim text-xs w-3">{open[s.name] ? '▼' : '▶'}</span>
            {#if !planning}
              <span class="w-2 h-2 rounded-full shrink-0 {dot(s.state)}"></span>
            {/if}
            <span class="font-medium text-fjord-fg-body min-w-[12rem]">{s.name}</span>
            <span class="text-xs text-fjord-fg-muted font-mono flex-1 truncate">{summary(s, edits[s.name] ?? [])}</span>
            {#if isolated.has(s.name)}
              <span
                title="Nothing else in this stack shares a network with {s.name}"
                class="shrink-0 text-[10px] font-semibold px-1.5 py-0.5 rounded bg-fjord-warning/15 text-fjord-warning border border-fjord-warning/30"
                >cut off</span
              >
            {/if}
          </button>
          {#if updates[s.name]}
            {@const u = updates[s.name]}
            <button
              on:click={() => dispatch('openUpdate')}
              title={u.state === 'upgrade'
                ? `A newer version of ${u.tag} is published — see what an update would change`
                : `A newer image is published for ${u.tag} — see what an update would change`}
              class="shrink-0 ml-3 inline-flex items-center gap-1 text-[10px] font-semibold px-1.5 py-0.5 rounded bg-fjord-warning/15 text-fjord-warning border border-fjord-warning/30 hover:bg-fjord-warning/25"
              ><Icon name="arrow-up" size={10} />{u.state === 'upgrade'
                ? `${u.fromVersion ? `v${u.fromVersion} → ` : ''}v${u.toVersion}`
                : 'update'}</button
            >
          {/if}
          {#if pinned[s.name]}
            <button
              on:click={() => dispatch('unpin', s.name)}
              title="Pinned to an exact image, so it takes no updates. Unpin to follow its tag again."
              class="shrink-0 ml-3 inline-flex items-center gap-1 text-[10px] font-semibold px-1.5 py-0.5 rounded bg-fjord-accent/15 text-fjord-accent border border-fjord-accent/30 hover:bg-fjord-accent/25"
              ><Icon name="pin" size={10} />pinned · unpin</button
            >
          {/if}
          {#if rollbacks[s.name] && !planning}
            <button
              on:click={() => (confirmRollback = confirmRollback === s.name ? '' : s.name)}
              title="Go back to the image {s.name} ran before its last update"
              class="shrink-0 ml-3 text-[10px] font-semibold px-1.5 py-0.5 rounded border border-fjord-border text-fjord-fg-muted hover:text-fjord-fg hover:bg-fjord-border"
              >↶ roll back</button
            >
          {/if}
          {#if !planning}<span class="shrink-0 text-xs text-fjord-fg-dim pl-3 pr-4">{s.state ?? ''}</span>{:else}<span class="pr-4"></span>{/if}
        </div>
        {#if confirmRollback === s.name && rollbacks[s.name]}
          <div class="flex items-center gap-3 mx-4 mb-3 px-3 py-2 rounded-lg bg-fjord-warning/10 border border-fjord-warning/30 text-xs text-fjord-fg-body">
            <span class="flex-1">
              Roll <b>{s.name}</b> back to <span class="font-mono">{rollbacks[s.name].ref}</span>, which it ran until
              {day(rollbacks[s.name].at)}? It is pinned there until you unpin it. This does not undo changes the new
              version made to its data — an app that migrated its database keeps the new schema.
            </span>
            <button on:click={() => (confirmRollback = '')} class="shrink-0 px-2 py-1 rounded text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
            <button
              on:click={() => {
                confirmRollback = '';
                dispatch('rollback', s.name);
              }}
              class="shrink-0 px-2 py-1 rounded bg-fjord-danger hover:bg-fjord-danger-hover text-white">Roll back</button
            >
          </div>
        {/if}

        {#if open[s.name]}
          <div class="px-10 pb-5 space-y-4">
            {#if s.detail}
              <p class="text-xs text-fjord-warning">{s.detail}</p>
            {/if}
            {#if !planning}
            <div class="grid grid-cols-[6rem_1fr] gap-x-3 gap-y-1 text-xs">
              <span class="text-fjord-fg-dim">image</span>
              <span class="font-mono text-fjord-fg-body break-all">
                {s.image ?? '—'}
                {#if versionable && s.image}
                  <button
                    on:click={() => dispatch('version', { service: s.name, image: s.image ?? '' })}
                    class="ml-2 font-sans text-fjord-accent hover:underline">Version…</button
                  >
                {/if}
              </span>
              <span class="text-fjord-fg-dim">container</span>
              <span class="font-mono text-fjord-fg-body">{s.container ?? '—'}</span>
            </div>
            {/if}

            <div>
              <div class="text-xs font-semibold text-fjord-fg-secondary mb-1">Networking</div>
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1 mb-2 text-xs">
                <label for="type-{s.name}" class="text-fjord-fg-dim">Type</label>
                <select
                  id="type-{s.name}"
                  value={serviceType(edits[s.name] ?? [])}
                  on:change={(e) => setType(s.name, e.currentTarget.value)}
                  class="bg-fjord-inset border border-fjord-border rounded-lg px-2 py-1.5 text-fjord-fg-body min-w-[10rem]"
                >
                  {#each TYPES as t}
                    <option value={t.id} disabled={unsupportedModes.includes(t.id) && t.id !== serviceType(edits[s.name] ?? [])}>
                      {t.id}{unsupportedModes.includes(t.id) ? ' — not on this engine' : ''}
                    </option>
                  {/each}
                </select>
                <span class="text-fjord-fg-dim">{TYPES.find((t) => t.id === serviceType(edits[s.name] ?? []))?.text}</span>
              </div>
              {#if serviceType(edits[s.name] ?? []) !== 'networks'}
                {#if (kept[s.name] ?? []).length}
                  <p class="text-xs text-fjord-fg-dim">
                    {(kept[s.name] ?? []).length} interface{(kept[s.name] ?? []).length > 1 ? 's' : ''} kept until Save — set
                    Type back to networks to get {(kept[s.name] ?? []).length > 1 ? 'them' : 'it'} back.
                  </p>
                {/if}
              {:else}
              <table class="w-full text-xs">
                <thead class="text-fjord-fg-dim">
                  <tr>
                    {#if !planning}<th class="text-left font-medium pb-1 w-24">Interface</th>{/if}
                    <th class="text-left font-medium pb-1">Network</th>
                    <th class="text-left font-medium pb-1">Address</th>
                    <th class="text-left font-medium pb-1">MAC</th>
                    <th class="pb-1"></th>
                  </tr>
                </thead>
                <tbody>
                  <!-- edits[...] inline, NOT rows(s.name): the template's
                       dependencies come from what it REFERENCES, and a
                       helper reading edits inside its body hides that --
                       add and remove then changed nothing on screen. -->
                  {#each edits[s.name] ?? [] as r, i}
                    {@const net = byName(r.network)}
                    {@const builtin = !r.network || r.network === 'bridge'}
                    {@const problem = duplicate(edits[s.name] ?? [], i)
                      ? `${r.network} is already on this service`
                      : addressProblem(r.ip ?? '', net) || address6Problem(r.ip6 ?? '', net)}
                    <tr class="border-t border-fjord-border/60 align-top">
                      {#if !planning}
                        <td class="py-1.5 pr-2 font-mono text-fjord-fg-dim">{r.iface ?? 'on create'}</td>
                      {/if}
                      <td class="py-1.5 pr-2">
                        <div class="flex items-center gap-2">
                          <span class="shrink-0 w-14 text-center font-mono text-[10px] px-1 py-0.5 rounded border border-fjord-border text-fjord-fg-muted">{kindOf(networks, r.network)}</span>
                          <!-- "" is bridge (no network named): shown as such, or
                               it matched no option and the picker was blank. -->
                          <select
                            value={r.network || 'bridge'}
                            on:change={(e) => setField(s.name, i, 'network', e.currentTarget.value)}
                            class="w-full bg-fjord-inset border border-fjord-border rounded-lg px-2 py-1.5 text-fjord-fg-body"
                          >
                            {#each netGroups(networks, r.network) as g}
                              <optgroup label={g.label}>
                                {#if g.label === 'bridge'}
                                  {@const blocked = bridgeAlone && (edits[s.name] ?? []).length > 1 && !builtin}
                                  <option value="bridge" disabled={blocked}>
                                    bridge — built in: NAT, published ports{blocked ? ' (only on its own on this engine)' : ''}
                                  </option>
                                  {#if offerPrivateSpec}
                                    <option value="private">private — this stack's own network, not reachable from your LAN</option>
                                  {/if}
                                {/if}
                                {#each g.nets as n}
                                  <!-- Say when a network carries IPv6. Showing only
                                       the v4 subnet made a dual-stack network read
                                       exactly like a v4-only one. -->
                                  {@const clash = sameBridgeAs(s.name, i, n)}
                                  <option value={n.name} disabled={!!clash}>
                                    {n.name}{n.subnet ? ` (${n.subnet}${n.subnet6 ? ' + IPv6' : ''})` : n.subnet6 ? ' (IPv6)' : ''}{clash
                                      ? ` — same bridge as ${clash}`
                                      : ''}
                                  </option>
                                {/each}
                              </optgroup>
                            {/each}
                            <!-- Keeps a network the host no longer defines, so an
                                 attachment is not silently lost. -->
                            {#if r.network && !byName(r.network) && !isBuiltIn(r.network) && !(offerPrivateSpec && r.network === 'private')}
                              <option value={r.network}>{r.network} (not on this host)</option>
                            {/if}
                          </select>
                        </div>
                      </td>
                      <td class="py-1.5 pr-2">
                        <!-- The built-in bridge's address is the engine's to give:
                             pinning one needs a subnet compose's default has not got. -->
                        <input
                          value={builtin ? '' : (r.ip ?? '')}
                          disabled={builtin}
                          on:input={(e) => setField(s.name, i, 'ip', e.currentTarget.value)}
                          placeholder={builtin
                            ? 'from the engine'
                            : `${s.live?.[r.network] ? s.live[r.network] + ' — ' : ''}${net?.addressSource === 'dhcp' ? 'from DHCP' : 'from the network'}`}
                          class="w-full bg-fjord-inset border rounded-lg px-2 py-1.5 font-mono text-fjord-fg-body disabled:opacity-60 {problem
                            ? 'border-fjord-danger'
                            : 'border-fjord-border'}"
                        />
                        <!-- Only where the network has a v6 segment: a box you
                             cannot put anything in is worse than no box. -->
                        {#if net && !net.subnet6 && r.network && !isBuiltIn(r.network)}
                          <div class="mt-1 text-[10px] text-fjord-fg-faint">IPv4 only — add an IPv6 segment on Networks</div>
                        {/if}
                        {#if net?.subnet6}
                          <input
                            value={r.ip6 ?? ''}
                            on:input={(e) => setField(s.name, i, 'ip6', e.currentTarget.value)}
                            placeholder="IPv6 — from the network"
                            class="mt-1 w-full bg-fjord-inset border rounded-lg px-2 py-1.5 font-mono text-fjord-fg-body {problem
                              ? 'border-fjord-danger'
                              : 'border-fjord-border'}"
                          />
                        {/if}
                      </td>
                      <td class="py-1.5 pr-2">
                        {#if builtin}
                          <input disabled placeholder="—" class="w-full bg-fjord-inset border border-fjord-border rounded-lg px-2 py-1.5 font-mono disabled:opacity-60" />
                        {:else}
                          <div class="flex gap-1">
                            <input
                              value={r.mac ?? ''}
                              on:input={(e) => setField(s.name, i, 'mac', e.currentTarget.value)}
                              placeholder="auto"
                              class="w-full bg-fjord-inset border border-fjord-border rounded-lg px-2 py-1.5 font-mono text-fjord-fg-body"
                            />
                            <button
                              on:click={() => genMAC(s.name, i)}
                              title="Generate a MAC"
                              class="shrink-0 px-2 rounded-lg bg-fjord-inset border border-fjord-border text-fjord-fg-muted hover:text-fjord-fg"
                              >Gen</button
                            >
                          </div>
                        {/if}
                      </td>
                      <td class="py-1.5 text-right whitespace-nowrap">
                        {#if confirmRemove === `${s.name}:${i}`}
                          <button
                            on:click={() => removeRow(s.name, i)}
                            class="ml-1 text-[10px] px-1.5 py-0.5 rounded bg-fjord-danger hover:bg-fjord-danger-hover text-white"
                            >Remove</button
                          >
                          <button
                            on:click={() => (confirmRemove = '')}
                            class="ml-1 text-[10px] px-1.5 py-0.5 rounded text-fjord-fg-muted hover:text-fjord-fg">Cancel</button
                          >
                        {:else}
                          <button
                            on:click={() => (confirmRemove = `${s.name}:${i}`)}
                            title="Remove this interface"
                            class="ml-1 align-middle text-fjord-fg-dim hover:text-fjord-danger transition-colors"
                            ><Icon name="trash" size={13} /></button
                          >
                        {/if}
                      </td>
                    </tr>
                    {#if confirmRemove === `${s.name}:${i}` && cutsOff(s.name, i)}
                      <tr>
                        {#if !planning}<td></td>{/if}
                        <td colspan="4" class="pb-1.5 text-fjord-warning">
                          {s.name} reaches the rest of this stack over {r.network || 'this interface'}. Remove it
                          and nothing else in the stack can talk to {s.name} — it will keep running and stop answering.
                        </td>
                      </tr>
                    {/if}
                    {#if problem || net}
                      <tr>
                        {#if !planning}<td></td>{/if}
                        <td colspan="4" class="pb-1.5">
                          {#if problem}
                            <span class="text-fjord-danger">{problem}</span>
                          {:else if net?.wireWarning}
                            <span class="text-fjord-warning">{net.wireWarning}</span>
                          {:else if usableRange(net)}
                            <span class="text-fjord-fg-dim">Usable: <span class="font-mono">{usableRange(net)}</span></span>
                          {/if}
                        </td>
                      </tr>
                    {/if}
                  {:else}
                    <tr class="border-t border-fjord-border/60">
                      <td colspan="5" class="py-2 text-fjord-fg-dim">
                        On no network. Nothing outside this host can reach it.
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
              {#if isolated.has(s.name)}
                <p class="text-xs text-fjord-warning mt-1">
                  Nothing else in this stack shares a network with <b>{s.name}</b>, so its own
                  parts can no longer reach it — it will keep running and stop answering them.
                  Put it back on a network one of them is also on.
                </p>
              {/if}
              <div class="mt-2 flex items-center gap-3">
                <button
                  on:click={() => addRow(s.name)}
                  disabled={bridgeAlone && (edits[s.name] ?? []).some((r) => !r.network || r.network === 'bridge')}
                  class="text-xs px-2.5 py-1 rounded-lg bg-fjord-inset border border-fjord-border text-fjord-fg-secondary hover:text-fjord-fg hover:border-fjord-accent/40 transition-colors disabled:opacity-40"
                  >+ interface</button
                >
                {#if bridgeAlone && (edits[s.name] ?? []).some((r) => !r.network || r.network === 'bridge')}
                  <span class="text-xs text-fjord-fg-dim">On this engine bridge stands alone — pick a network instead to add more.</span>
                {/if}
              </div>
              {/if}
          </div>

          {#if !planning}
          <div>
            <div class="text-xs font-semibold text-fjord-fg-secondary mb-1">Storage</div>
            {#if (s.volumes ?? []).length}
              <table class="w-full text-xs">
                <tbody>
                  {#each s.volumes ?? [] as v}
                    <tr class="border-t border-fjord-border/60">
                      <td class="py-1 pr-3 font-mono text-fjord-fg-body break-all">{mountLabel(v)}</td>
                      <td class="py-1 pr-2 text-fjord-fg-dim">→</td>
                      <td class="py-1 pr-3 font-mono text-fjord-fg-body">{v.dest}</td>
                      <td class="py-1 text-right whitespace-nowrap">
                        {#if v.readOnly}<span class="text-[10px] px-1 rounded bg-fjord-inset text-fjord-fg-dim mr-1">RO</span>{/if}
                        <span class="text-[10px] px-1 rounded bg-fjord-inset text-fjord-fg-dim">{v.kind ?? ''}</span>
                        {#if confirmUnmount === `${s.name}:${v.dest}`}
                          <button
                            on:click={() => { dispatch('unmount', { service: s.name, dest: v.dest ?? '' }); confirmUnmount = ''; }}
                            class="ml-1 text-[10px] px-1.5 py-0.5 rounded bg-fjord-danger hover:bg-fjord-danger-hover text-white"
                            >Unmount</button
                          >
                          <button
                            on:click={() => (confirmUnmount = '')}
                            class="ml-1 text-[10px] px-1.5 py-0.5 rounded text-fjord-fg-muted hover:text-fjord-fg">Cancel</button
                          >
                        {:else}
                          <button
                            on:click={() => (confirmUnmount = `${s.name}:${v.dest}`)}
                            title="Unmount this — the files stay, the stack stops seeing them"
                            class="ml-1 align-middle text-fjord-fg-dim hover:text-fjord-danger transition-colors"
                            ><Icon name="trash" size={13} /></button
                          >
                        {/if}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            {:else}
              <p class="text-xs text-fjord-fg-dim">Nothing mounted.</p>
            {/if}
            {#if addingTo === s.name}
              <AddMount
                service={s.name}
                {canUseVolumes}
                {namedVolumes}
                {folderSets}
                bind:source={addSource}
                on:add
                on:remote
                on:folderset
                on:browse
                on:cancel={() => dispatch('closeAdd')}
              />
            {:else}
              <button
                on:click={() => dispatch('openAdd', s.name)}
                class="mt-2 text-xs px-2.5 py-1 rounded-lg bg-fjord-inset border border-fjord-border text-fjord-fg-secondary hover:text-fjord-fg hover:border-fjord-accent/40 transition-colors"
                >+ Add…</button
              >
            {/if}
          </div>
          {/if}
        </div>
      {/if}
    </div>
  {/each}
</div>
{/if}
