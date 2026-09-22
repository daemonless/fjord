<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import EmptyState from './EmptyState.svelte';
  import { toast } from './toast';

  type Volume = {
    name: string;
    driver: string;
    kind?: string;
    anonymous?: boolean;
    mountpoint: string;
    options?: Record<string, string>;
    createdAt?: string;
    usedBy?: string[];
  };

  let volumes: Volume[] = [];
  let loading = true;
  let error = '';
  let showAnonymous = false;

  // The backend classifies volumes (kind, anonymous) -- runtime naming
  // conventions are its business, not the UI's.
  const isNFS = (v: Volume) => v.kind === 'nfs';

  $: named = volumes.filter((v) => showAnonymous || !v.anonymous);
  $: anonCount = volumes.filter((v) => v.anonymous).length;

  async function load() {
    loading = true;
    error = '';
    try {
      const res = await fetch('/api/volumes');
      if (!res.ok) throw new Error(await res.text());
      volumes = await res.json();
    } catch (e: any) {
      error = e.message || 'Failed to load volumes';
    } finally {
      loading = false;
    }
  }

  onMount(load);

  // ---- create ----
  let creating = false;
  let form = { name: '', type: 'local', server: '', path: '', user: '', password: '', ro: false };
  // Host OS, for the SMB caveat: FreeBSD's built-in client speaks SMB1 only.
  let hostOS = '';
  fetch('/api/about').then((r) => (r.ok ? r.json() : null)).then((d) => (hostOS = d?.os || '')).catch(() => {});
  let submitting = false;
  let createError = '';

  function openCreate() {
    form = { name: '', type: 'local', server: '', path: '', user: '', password: '', ro: false };
    createError = '';
    creating = true;
  }

  $: nfsPreview = form.server && form.path ? `${form.server}:${form.path.startsWith('/') ? '' : '/'}${form.path}` : '';
  $: smbPreview = form.server && form.path ? `//${form.user.trim() || 'guest'}@${form.server}/${form.path.replace(/^\/+/, '')}` : '';

  async function submitCreate() {
    if (!form.name.trim()) return;
    submitting = true;
    createError = '';
    // Runtime-neutral spec: the backend translates kind/server/path into its
    // platform's mount options (FreeBSD vs Linux NFS syntax differs).
    const body: any = { name: form.name.trim() };
    if (form.type === 'nfs') {
      if (!form.server.trim() || !form.path.trim()) {
        createError = 'NFS needs a server and export path';
        submitting = false;
        return;
      }
      body.kind = 'nfs';
      body.server = form.server.trim();
      body.path = form.path.trim();
      body.readOnly = form.ro;
    } else if (form.type === 'smb') {
      if (!form.server.trim() || !form.path.trim()) {
        createError = 'SMB needs a server and share name';
        submitting = false;
        return;
      }
      body.kind = 'smb';
      body.server = form.server.trim();
      body.path = form.path.trim();
      body.user = form.user.trim();
      body.password = form.password; // stored root-only by the daemon, never shown again
      body.readOnly = form.ro;
    }
    try {
      const res = await fetch('/api/volumes', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error(await res.text());
      creating = false;
      await load();
    } catch (e: any) {
      createError = e.message || 'Create failed';
    } finally {
      submitting = false;
    }
  }

  // ---- delete (two-click confirm) ----
  let confirmDelete = '';
  let forceDelete = ''; // set after a 409: the row offers a force-remove instead
  async function del(name: string, force = false) {
    try {
      const q = force ? '?force=true' : '';
      const res = await fetch(`/api/volumes/${encodeURIComponent(name)}${q}`, { method: 'DELETE' });
      // In-use: surface the clean message and turn the row into a force-remove.
      if (res.status === 409 && !force) {
        toast(await res.text(), { kind: 'error' });
        forceDelete = name;
        return;
      }
      if (!res.ok) throw new Error(await res.text());
      confirmDelete = '';
      forceDelete = '';
      await load();
    } catch (e: any) {
      toast(e.message || 'Delete failed', { kind: 'error' });
    }
  }
</script>

<div class="h-full flex flex-col">
  <div class="flex items-center justify-between mb-4 shrink-0">
    <div>
      <h2 class="text-2xl font-bold text-fjord-fg">Volumes</h2>
      <p class="text-sm text-fjord-fg-dim">
        Engine-managed storage — local and NFS shares. Attach one to a stack from that stack's
        <b class="text-fjord-fg-muted">Resources</b> tab (podman stacks only).
      </p>
    </div>
    <button
      on:click={openCreate}
      class="flex items-center gap-2 bg-fjord-accent hover:bg-fjord-accent-hover text-white font-medium py-2 px-4 rounded-lg text-sm"
      ><Icon name="plus" size={14} /> New Volume…</button
    >
  </div>

  {#if anonCount > 0}
    <label class="flex items-center gap-2 text-xs text-fjord-fg-dim mb-3 shrink-0 cursor-pointer select-none">
      <input type="checkbox" bind:checked={showAnonymous} class="accent-fjord-accent" />
      Show {anonCount} anonymous volume{anonCount === 1 ? '' : 's'} (container-managed)
    </label>
  {/if}

  <div class="flex-1 overflow-y-auto">
    {#if loading}
      <div class="flex items-center gap-3 text-fjord-fg-dim text-sm"><Spinner size={18} /> Loading…</div>
    {:else if error}
      <EmptyState icon="alert" title="Volumes Unavailable" description={error} />
    {:else if named.length === 0}
      <EmptyState
        icon="drive"
        title="No Volumes"
        description="Create a named volume to mount local or NFS storage into your stacks."
        actionLabel="New Volume…"
        on:action={openCreate}
      />
    {:else}
      <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border">
        {#each named as v}
          <div class="flex items-center gap-3 px-4 py-3">
            <div class="shrink-0 text-fjord-fg-muted"><Icon name={isNFS(v) ? 'globe' : 'drive'} size={18} /></div>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="font-mono text-sm text-fjord-fg truncate">{v.name}</span>
                {#if isNFS(v)}
                  <span class="text-[11px] font-medium bg-fjord-accent/20 text-fjord-accent px-1.5 py-0.5 rounded">NFS</span>
                {/if}
              </div>
              <div class="text-xs text-fjord-fg-dim font-mono truncate">
                {#if isNFS(v)}{v.options?.device}{v.options?.o ? ` (${v.options.o})` : ''}{:else}{v.mountpoint}{/if}
              </div>
            </div>
            {#if v.usedBy?.length}
              <div class="flex items-center gap-1 shrink-0" title="Stacks mounting this volume">
                {#each v.usedBy as st}
                  <span class="text-[10px] px-1.5 py-0.5 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-muted">{st}</span>
                {/each}
              </div>
            {/if}
            {#if v.usedBy?.length}
              <span class="text-xs px-2 py-1 text-fjord-fg-faint cursor-not-allowed" title="In use by {v.usedBy.join(', ')} — detach it from those stacks first">Delete</span>
            {:else if forceDelete === v.name}
              <button on:click={() => del(v.name, true)} class="text-xs px-2 py-1 rounded bg-fjord-danger hover:bg-fjord-danger-hover text-white" title="Remove even though a container has it mounted">Force Remove</button>
              <button on:click={() => (forceDelete = '')} class="text-xs px-2 py-1 rounded text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
            {:else if confirmDelete === v.name}
              <button on:click={() => del(v.name)} class="text-xs px-2 py-1 rounded bg-fjord-danger hover:bg-fjord-danger-hover text-white">Delete</button>
              <button on:click={() => (confirmDelete = '')} class="text-xs px-2 py-1 rounded text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
            {:else}
              <button
                on:click={() => (confirmDelete = v.name)}
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
      <h3 class="text-lg font-bold text-fjord-fg mb-4">New Volume</h3>

      <div class="flex flex-col gap-4">
        <div class="flex flex-col gap-1">
          <label class="text-sm font-semibold text-fjord-fg-secondary" for="v-name">Name</label>
          <input
            id="v-name"
            bind:value={form.name}
            placeholder="media, backups, …"
            class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent"
          />
        </div>

        <div class="flex gap-2">
          <button
            on:click={() => (form.type = 'local')}
            class="flex-1 flex items-center justify-center gap-2 px-3 py-2 rounded-md text-sm font-medium border transition-colors {form.type ===
            'local'
              ? 'bg-fjord-accent/20 text-fjord-accent border-fjord-accent/50'
              : 'border-fjord-border text-fjord-fg-secondary hover:bg-fjord-border'}"><Icon name="drive" size={14} /> Local</button
          >
          <button
            on:click={() => (form.type = 'nfs')}
            class="flex-1 flex items-center justify-center gap-2 px-3 py-2 rounded-md text-sm font-medium border transition-colors {form.type ===
            'nfs'
              ? 'bg-fjord-accent/20 text-fjord-accent border-fjord-accent/50'
              : 'border-fjord-border text-fjord-fg-secondary hover:bg-fjord-border'}"><Icon name="globe" size={14} /> NFS</button
          >
          <button
            on:click={() => (form.type = 'smb')}
            class="flex-1 flex items-center justify-center gap-2 px-3 py-2 rounded-md text-sm font-medium border transition-colors {form.type ===
            'smb'
              ? 'bg-fjord-accent/20 text-fjord-accent border-fjord-accent/50'
              : 'border-fjord-border text-fjord-fg-secondary hover:bg-fjord-border'}"><Icon name="globe" size={14} /> SMB</button
          >
        </div>

        {#if form.type === 'smb'}
          <div class="grid grid-cols-2 gap-2">
            <div class="flex flex-col gap-1">
              <label class="text-xs font-semibold text-fjord-fg-muted" for="v-smb-server">Server</label>
              <input id="v-smb-server" bind:value={form.server} placeholder="mars" class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent" />
            </div>
            <div class="flex flex-col gap-1">
              <label class="text-xs font-semibold text-fjord-fg-muted" for="v-smb-share">Share</label>
              <input id="v-smb-share" bind:value={form.path} placeholder="media" class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent" />
            </div>
            <div class="flex flex-col gap-1">
              <label class="text-xs font-semibold text-fjord-fg-muted" for="v-smb-user">User</label>
              <input id="v-smb-user" bind:value={form.user} placeholder="guest" autocomplete="off" class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent" />
            </div>
            <div class="flex flex-col gap-1">
              <label class="text-xs font-semibold text-fjord-fg-muted" for="v-smb-pass">Password</label>
              <input id="v-smb-pass" type="password" bind:value={form.password} placeholder="stored on the host, root-only" autocomplete="new-password" class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent" />
            </div>
          </div>
          <label class="flex items-center gap-2 text-sm text-fjord-fg-secondary cursor-pointer select-none">
            <input type="checkbox" bind:checked={form.ro} class="accent-fjord-accent" /> Read-only
          </label>
          {#if smbPreview}
            <p class="text-xs text-fjord-fg-dim">mounts <span class="font-mono text-fjord-fg-muted">{smbPreview}</span> — leave the password empty to reuse one already stored for this user and server.</p>
          {/if}
          {#if hostOS === 'freebsd'}
            <p class="flex items-start gap-1.5 text-xs text-fjord-warning"><Icon name="alert" size={12} class="shrink-0 mt-0.5" /><span>FreeBSD's built-in SMB client only speaks SMB1, which most servers refuse. If the server also exports NFS, use that instead.</span></p>
          {/if}
        {/if}

        {#if form.type === 'nfs'}
          <div class="grid grid-cols-3 gap-2">
            <div class="flex flex-col gap-1">
              <label class="text-xs font-semibold text-fjord-fg-muted" for="v-server">Server</label>
              <input
                id="v-server"
                bind:value={form.server}
                placeholder="mars"
                class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent"
              />
            </div>
            <div class="col-span-2 flex flex-col gap-1">
              <label class="text-xs font-semibold text-fjord-fg-muted" for="v-path">Export path</label>
              <input
                id="v-path"
                bind:value={form.path}
                placeholder="/mnt/tide"
                class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent"
              />
            </div>
          </div>
          <label class="flex items-center gap-2 text-sm text-fjord-fg-secondary cursor-pointer select-none">
            <input type="checkbox" bind:checked={form.ro} class="accent-fjord-accent" /> Read-only
          </label>
          {#if nfsPreview}
            <p class="text-xs text-fjord-fg-dim">mounts <span class="font-mono text-fjord-fg-muted">{nfsPreview}</span></p>
          {/if}
        {/if}

        {#if createError}<p class="text-xs text-fjord-danger">{createError}</p>{/if}
      </div>

      <div class="flex justify-end gap-3 mt-6">
        <button on:click={() => (creating = false)} class="px-4 py-2 rounded-md font-medium text-fjord-fg-secondary hover:bg-fjord-border transition-colors">Cancel</button>
        <button
          on:click={submitCreate}
          disabled={submitting || !form.name.trim()}
          class="px-4 py-2 rounded-md font-medium text-white bg-fjord-accent hover:bg-fjord-accent-hover transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >{submitting ? 'Creating…' : 'Create'}</button
        >
      </div>
    </div>
  </div>
{/if}
