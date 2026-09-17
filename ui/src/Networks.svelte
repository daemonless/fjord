<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import EmptyState from './EmptyState.svelte';
  import { toast } from './toast';
  import FixSnippet from './FixSnippet.svelte';

  type Network = { name: string; driver: string; subnet?: string; gateway?: string; usedBy?: string[]; problem?: string; engines?: string[] };
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
    supportsDhcp?: boolean;
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

  async function load() {
    loading = true;
    error = '';
    try {
      const res = await fetch('/api/networks');
      if (!res.ok) throw new Error(await res.text());
      networks = await res.json();
      // Kinds and parents are advisory: a failure here disables creating but
      // must not hide the networks that already exist.
      await refreshHost();
    } catch (e: any) {
      error = e.message || 'Failed to load networks';
    } finally {
      loading = false;
    }
  }

  // What the host looks like right now: which bridges exist, and what the
  // setup snippets should suggest next. Re-read whenever the create dialog
  // opens -- it tells the user to go make a bridge and come back, so the
  // answer is expected to have changed since the page loaded.
  async function refreshHost() {
    const k = await fetch('/api/networks/kinds').then((r) => (r.ok ? r.json() : null)).catch(() => null);
    kinds = k?.kinds ?? [];
    kindsNote = k?.note ?? '';
    canRemove = k?.canRemove ?? true;
    parents = await fetch('/api/networks/parents?engine=podman').then((r) => (r.ok ? r.json() : [])).catch(() => []);
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
  let form = { name: '', parent: '', subnet: '', gateway: '', mtu: '', rangeStart: '', rangeEnd: '', description: '' };
  // "dhcp" = the segment's own server allocates; "pool" = this host does.
  let addressSource: 'dhcp' | 'pool' = 'dhcp';
  let advanced = false;
  // The bridge-setup help, available whether or not a bridge already exists.
  let showSetup = false;
  // A working copy of the kind's setups: the host guesses the NIC and the VLAN
  // id, the user corrects them, and the backend re-renders that one snippet.
  let setups: Setup[] = [];
  let setupBusy = '';
  let rechecking = false;

  // The user runs the commands in another window; nothing tells fjord when
  // they are done, so give them a way to say so without losing the dialog.
  async function recheck() {
    rechecking = true;
    try {
      await refreshHost();
      if (parents.length) {
        showSetup = !parents.some((p) => !p.inUse);
        toast.success(`Found ${parents.length} ${parents.length === 1 ? 'bridge' : 'bridges'}`);
      } else {
        toast.error('Still no bridge on this host');
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
      toast.error(`${e}`);
    } finally {
      setupBusy = '';
    }
  }

  $: kind = kinds.find((k) => k.id === kindID) || kinds[0];
  $: needsParent = !!kind?.parentLabel;
  $: isPrivate = kind?.id === 'nat';
  // A LAN network has nowhere to attach without a bridge; say so rather than
  // letting someone fill in a form that cannot succeed.
  $: blocked = needsParent && parents.length === 0;

  async function openCreate() {
    form = { name: '', parent: '', subnet: '', gateway: '', mtu: '', rangeStart: '', rangeEnd: '', description: '' };
    advanced = false;
    createError = '';
    creating = true;
    await refreshHost();
    kindID = kinds[0]?.id || '';
    // One allocator beats two on the same wire, so DHCP leads where the
    // plugin can do it. A pool is the fallback, not the default.
    addressSource = kinds[0]?.supportsDhcp ? 'dhcp' : 'pool';
    showSetup = parents.length > 0 && parents.every((p) => p.inUse);
  }

  // Offer the usual .1 for a /24 so the common case is one less field to fill.
  function guessGateway() {
    const m = form.subnet.match(/^(\d+\.\d+\.\d+)\.\d+\/\d+$/);
    if (m && !form.gateway) form.gateway = m[1] + '.1';
  }

  $: chosenParent = parents.find((p) => p.name === form.parent);

  // Picking a parent fills in what the host already knows: the segment's
  // subnet and gateway, a name following the bridge, and a default range.
  // Only empty fields are touched, so nothing typed is overwritten.
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
    if (addressSource !== 'pool') return;
    if (!form.subnet.trim() && p.subnet) form.subnet = p.subnet;
    if (!form.gateway.trim() && p.gateway) form.gateway = p.gateway;
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
  $: canSubmit = form.name.trim() && !nameTaken && (!needsParent || form.parent) &&
    (isPrivate ? !!form.subnet.trim() : addressSource === 'dhcp' || form.subnet.trim());

  async function submitCreate() {
    if (!canSubmit) return;
    submitting = true;
    createError = '';
    const body: any = { name: form.name.trim(), kind: kind?.id, addressSource };
    const eng = kind?.engine ? '?engine=' + encodeURIComponent(kind.engine) : '';
    if (needsParent) body.parent = form.parent;
    if (addressSource === 'pool') {
      body.subnet = form.subnet.trim();
      if (kind?.needsGateway) body.gateway = form.gateway.trim();
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
    {:else if networks.length === 0}
      <EmptyState
        icon="globe"
        title="No Networks"
        description={kinds.length === 0
          ? kindsNote || 'This engine cannot create networks on this host.'
          : 'Create one to give stacks their own address on your network.'}
        actionLabel={kinds.length > 0 ? 'New Network…' : undefined}
        on:action={openCreate}
      />
    {:else}
      <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border">
        {#each networks as n}
          <div class="flex items-center gap-3 px-4 py-3">
            <div class="shrink-0 text-fjord-fg-muted"><Icon name="globe" size={18} /></div>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="font-mono text-sm text-fjord-fg truncate">{n.name}</span>
                <span class="text-[11px] font-medium bg-fjord-accent/20 text-fjord-accent px-1.5 py-0.5 rounded">{n.driver}</span>
              </div>
              <div class="text-xs text-fjord-fg-dim font-mono truncate">
                {n.subnet || 'DHCP'}{n.gateway ? ` · gw ${n.gateway}` : ''}
              </div>
              {#if n.problem}
                <div class="text-xs text-fjord-warning mt-0.5">{n.problem}</div>
              {/if}
            </div>
            {#if n.engines?.length}
              <div class="shrink-0 text-xs text-fjord-fg-dim font-mono" title="Engines that can attach a stack to this network">
                {n.engines.join(' · ')}
              </div>
            {/if}
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
        {/each}
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
        {#if kind?.help}
          <p class="text-xs text-fjord-fg-dim -mt-2">{kind.help}</p>
        {/if}

        {#snippet setupCard(ps, i)}
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
              <input id="n-priv" bind:value={form.subnet} placeholder="10.100.0.0/24" class={inputCls} />
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
                  <option value={p.name}>{p.name}{p.inUse ? ' (already has a network)' : ''}</option>
                {/each}
                {#if kind.parentSetups?.length}
                  <option value={NEW_PARENT}>+ Set up a new {kind.parentLabel?.toLowerCase()}…</option>
                {/if}
              </select>
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

          <button
            type="button"
            on:click={() => (advanced = !advanced)}
            class="flex items-center gap-1.5 text-xs text-fjord-fg-muted hover:text-fjord-fg self-start"
          >
            <Icon name={advanced ? 'chevron-up' : 'chevron-down'} size={12} /> Advanced
          </button>

          {#if !advanced && addressSource === 'dhcp'}
            <p class="text-xs text-fjord-fg-dim -mt-1">
              Addresses come from the DHCP server on that segment, using each container's MAC — so
              your existing reservations apply and nothing here has to know the subnet.
            </p>
          {/if}

          {#if advanced}
            {#if kind?.supportsDhcp && !isPrivate}
              <div class="flex flex-col gap-1">
                <span class="text-xs font-semibold text-fjord-fg-muted">Addresses</span>
                <div class="flex gap-2">
                  <button
                    type="button"
                    on:click={() => (addressSource = 'dhcp')}
                    class="flex-1 px-3 py-2 rounded-md text-sm font-medium border transition-colors {addressSource ===
                    'dhcp'
                      ? 'bg-fjord-accent/20 text-fjord-accent border-fjord-accent/50'
                      : 'border-fjord-border text-fjord-fg-secondary hover:bg-fjord-border'}"
                    >From my DHCP server</button
                  >
                  <button
                    type="button"
                    on:click={() => { addressSource = 'pool'; applyParent(); }}
                    class="flex-1 px-3 py-2 rounded-md text-sm font-medium border transition-colors {addressSource ===
                    'pool'
                      ? 'bg-fjord-accent/20 text-fjord-accent border-fjord-accent/50'
                      : 'border-fjord-border text-fjord-fg-secondary hover:bg-fjord-border'}"
                    >A pool fjord manages</button
                  >
                </div>
              </div>
            {/if}

            {#if addressSource === 'pool' && !isPrivate}
              <div class="grid grid-cols-2 gap-2">
                <div class="flex flex-col gap-1">
                  <label class="text-xs font-semibold text-fjord-fg-muted" for="n-subnet">Subnet</label>
                  <input id="n-subnet" bind:value={form.subnet} on:blur={() => { guessGateway(); defaultRange(); }} placeholder="192.168.4.0/24" class={inputCls} />
                </div>
                {#if kind?.needsGateway}
                  <div class="flex flex-col gap-1">
                    <label class="text-xs font-semibold text-fjord-fg-muted" for="n-gw">Gateway</label>
                    <input id="n-gw" bind:value={form.gateway} placeholder="192.168.4.1" class={inputCls} />
                  </div>
                {/if}
              </div>

              {#if kind?.supportsRange}
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
                  fjord hands out addresses from this range. Your router does not know about it, so keep
                  it clear of whatever it leases.{#if chosenParent?.hostIp}
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

        <div class="flex justify-end gap-2 pt-2">
          <button on:click={() => (creating = false)} class="px-4 py-2 rounded-md text-sm text-fjord-fg-secondary hover:bg-fjord-border">Cancel</button>
          <button
            on:click={submitCreate}
            disabled={submitting || blocked || !canSubmit}
            class="px-4 py-2 rounded-md text-sm font-medium bg-fjord-accent hover:bg-fjord-accent-hover text-white disabled:opacity-50 disabled:cursor-not-allowed"
            >{submitting ? 'Creating…' : 'Create'}</button
          >
        </div>
      </div>
    </div>
  </div>
{/if}
