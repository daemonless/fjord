<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import EmptyState from './EmptyState.svelte';
  import { toast } from './toast';

  type Network = { name: string; driver: string; subnet?: string; gateway?: string; usedBy?: string[] };
  // A kind is the engine's own declaration of what it can create and which
  // fields that shape uses -- the form is built from this rather than from
  // anything the UI knows about a specific runtime.
  type Kind = {
    id: string;
    label: string;
    help?: string;
    parentLabel?: string;
    needsGateway?: boolean;
    supportsMtu?: boolean;
    supportsRange?: boolean;
    supportsDescription?: boolean;
  };
  type Parent = { name: string; inUse?: boolean };

  // Which engine's networks to show. Each engine has its own view: podman
  // reads the conflists, appjail reads those plus its own virtualnets, and a
  // network can be creatable on one and not the other. Defaulting to the
  // server's choice left the other engine's networks unreachable from here.
  let engine = '';
  let engines: { name: string; available: boolean; default: boolean }[] = [];
  $: availableEngines = engines.filter((e) => e.available);

  let networks: Network[] = [];
  let kinds: Kind[] = [];
  let parents: Parent[] = [];
  let loading = true;
  let error = '';

  $: q = engine ? '?engine=' + encodeURIComponent(engine) : '';

  async function load() {
    loading = true;
    error = '';
    try {
      const res = await fetch('/api/networks' + q);
      if (!res.ok) throw new Error(await res.text());
      networks = await res.json();
      // Kinds and parents are advisory: a failure here disables creating but
      // must not hide the networks that already exist.
      kinds = await fetch('/api/networks/kinds' + q).then((r) => (r.ok ? r.json() : [])).catch(() => []);
      parents = await fetch('/api/networks/parents' + q).then((r) => (r.ok ? r.json() : [])).catch(() => []);
    } catch (e: any) {
      error = e.message || 'Failed to load networks';
    } finally {
      loading = false;
    }
  }

  onMount(async () => {
    try {
      const res = await fetch('/api/engine');
      if (res.ok) {
        const d = await res.json();
        engines = d.engines || [];
        engine = d.default || '';
      }
    } catch {
      // no engine info -> the server falls back to its default
    }
    await load();
  });

  // ---- create ----
  let creating = false;
  let submitting = false;
  let createError = '';
  let kindID = '';
  let form = { name: '', parent: '', subnet: '', gateway: '', mtu: '', rangeStart: '', rangeEnd: '', description: '' };

  $: kind = kinds.find((k) => k.id === kindID) || kinds[0];
  $: needsParent = !!kind?.parentLabel;
  // A LAN network has nowhere to attach without a bridge; say so rather than
  // letting someone fill in a form that cannot succeed.
  $: blocked = needsParent && parents.length === 0;

  function openCreate() {
    form = { name: '', parent: '', subnet: '', gateway: '', mtu: '', rangeStart: '', rangeEnd: '', description: '' };
    kindID = kinds[0]?.id || '';
    createError = '';
    creating = true;
  }

  // Offer the usual .1 for a /24 so the common case is one less field to fill.
  function guessGateway() {
    const m = form.subnet.match(/^(\d+\.\d+\.\d+)\.\d+\/\d+$/);
    if (m && !form.gateway) form.gateway = m[1] + '.1';
  }

  async function submitCreate() {
    if (!form.name.trim()) return;
    submitting = true;
    createError = '';
    const body: any = { name: form.name.trim(), kind: kind?.id, subnet: form.subnet.trim() };
    if (needsParent) body.parent = form.parent;
    if (kind?.needsGateway) body.gateway = form.gateway.trim();
    if (kind?.supportsMtu && form.mtu.trim()) body.mtu = parseInt(form.mtu, 10);
    if (kind?.supportsRange && form.rangeStart.trim()) body.rangeStart = form.rangeStart.trim();
    if (kind?.supportsRange && form.rangeEnd.trim()) body.rangeEnd = form.rangeEnd.trim();
    if (kind?.supportsDescription && form.description.trim()) body.description = form.description.trim();
    try {
      const res = await fetch('/api/networks' + q, {
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
      const res = await fetch(`/api/networks/${encodeURIComponent(name)}${q}`, { method: 'DELETE' });
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
      {#if availableEngines.length > 1}
        <select
          bind:value={engine}
          on:change={load}
          title="Which engine's networks to show"
          class="bg-fjord-inset border border-fjord-border rounded-md px-2 py-2 text-sm text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
        >
          {#each availableEngines as e}
            <option value={e.name}>{e.name}</option>
          {/each}
        </select>
      {/if}
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
          ? 'This engine cannot create networks on this host.'
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
                {n.subnet || '—'}{n.gateway ? ` · gw ${n.gateway}` : ''}
              </div>
            </div>
            {#if n.usedBy?.length}
              <div class="flex items-center gap-1 shrink-0 max-w-[45%] overflow-hidden" title="Attached: {n.usedBy.join(', ')}">
                {#each n.usedBy.slice(0, 3) as c}
                  <span class="text-[10px] px-1.5 py-0.5 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-muted">{c}</span>
                {/each}
                {#if n.usedBy.length > 3}
                  <span class="text-[10px] text-fjord-fg-dim">+{n.usedBy.length - 3}</span>
                {/if}
              </div>
            {/if}
            {#if n.usedBy?.length}
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
    <div class="bg-fjord-card border border-fjord-border rounded-xl shadow-2xl w-full max-w-md p-6">
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

        {#if blocked}
          <div class="text-sm text-fjord-fg-secondary bg-fjord-inset border border-fjord-border rounded-md p-3">
            No {kind.parentLabel?.toLowerCase()} is available on this host. fjord does not create one —
            it is persistent host configuration. Add it to <code class="font-mono text-fjord-fg-muted">/etc/rc.conf</code>
            and bring it up, then reopen this dialog.
          </div>
        {:else}
          <div class="flex flex-col gap-1">
            <label class="text-sm font-semibold text-fjord-fg-secondary" for="n-name">Name</label>
            <input id="n-name" bind:value={form.name} placeholder="vlan4, lan, …" class={inputCls} />
          </div>

          {#if needsParent}
            <div class="flex flex-col gap-1">
              <label class="text-sm font-semibold text-fjord-fg-secondary" for="n-parent">{kind.parentLabel}</label>
              <select id="n-parent" bind:value={form.parent} class={inputCls}>
                <option value="">Select a {kind.parentLabel?.toLowerCase()}…</option>
                {#each parents as p}
                  <option value={p.name}>{p.name}{p.inUse ? ' (already has a network)' : ''}</option>
                {/each}
              </select>
            </div>
          {/if}

          <div class="grid grid-cols-2 gap-2">
            <div class="flex flex-col gap-1">
              <label class="text-xs font-semibold text-fjord-fg-muted" for="n-subnet">Subnet</label>
              <input id="n-subnet" bind:value={form.subnet} on:blur={guessGateway} placeholder="192.168.4.0/24" class={inputCls} />
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
                <label class="text-xs font-semibold text-fjord-fg-muted" for="n-rs">Range start <span class="font-normal">(optional)</span></label>
                <input id="n-rs" bind:value={form.rangeStart} placeholder="192.168.4.200" class={inputCls} />
              </div>
              <div class="flex flex-col gap-1">
                <label class="text-xs font-semibold text-fjord-fg-muted" for="n-re">Range end</label>
                <input id="n-re" bind:value={form.rangeEnd} placeholder="192.168.4.250" class={inputCls} />
              </div>
            </div>
            <p class="text-xs text-fjord-fg-dim -mt-2">
              Confine automatic allocation to part of the subnet, so it cannot collide with DHCP or
              static addresses.
            </p>
          {/if}

          <div class="grid grid-cols-2 gap-2">
            {#if kind?.supportsMtu}
              <div class="flex flex-col gap-1">
                <label class="text-xs font-semibold text-fjord-fg-muted" for="n-mtu">MTU <span class="font-normal">(optional)</span></label>
                <input id="n-mtu" bind:value={form.mtu} placeholder="1500" class={inputCls} />
              </div>
            {/if}
            {#if kind?.supportsDescription}
              <div class="flex flex-col gap-1">
                <label class="text-xs font-semibold text-fjord-fg-muted" for="n-desc">Description</label>
                <input id="n-desc" bind:value={form.description} placeholder="optional" class={inputCls} />
              </div>
            {/if}
          </div>
        {/if}

        {#if createError}
          <p class="text-sm text-fjord-danger">{createError}</p>
        {/if}

        <div class="flex justify-end gap-2 pt-2">
          <button on:click={() => (creating = false)} class="px-4 py-2 rounded-md text-sm text-fjord-fg-secondary hover:bg-fjord-border">Cancel</button>
          <button
            on:click={submitCreate}
            disabled={submitting || blocked || !form.name.trim()}
            class="px-4 py-2 rounded-md text-sm font-medium bg-fjord-accent hover:bg-fjord-accent-hover text-white disabled:opacity-50 disabled:cursor-not-allowed"
            >{submitting ? 'Creating…' : 'Create'}</button
          >
        </div>
      </div>
    </div>
  </div>
{/if}
