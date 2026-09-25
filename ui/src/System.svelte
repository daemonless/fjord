<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import EmptyState from './EmptyState.svelte';
  import InstallCheckButton from './InstallCheckButton.svelte';
  import FixSnippet from './FixSnippet.svelte';
  import { toast } from './toast';

  type CheckResult = {
    id: string;
    name: string;
    status: 'ok' | 'warn' | 'fail' | 'unknown';
    detail?: string;
    why?: string;
    fix?: string;
    installable?: boolean;
  };
  type Report = {
    os: string;
    mode: string;
    canInstall: boolean;
    checks: CheckResult[];
  };

  let report: Report | null = null;
  let engineName = '';
  let daemonVersion = '';
  let loading = true;
  let error = '';

  async function load() {
    loading = true;
    error = '';
    try {
      const [setupRes, engineRes, aboutRes] = await Promise.all([
        fetch('/api/setup'),
        fetch('/api/engine'),
        fetch('/api/about'),
      ]);
      if (!setupRes.ok) throw new Error(await setupRes.text());
      report = await setupRes.json();
      if (engineRes.ok) engineName = (await engineRes.json()).default || '';
      if (aboutRes.ok) daemonVersion = (await aboutRes.json()).version || '';
    } catch (e: any) {
      error = e.message || 'Failed to load system report';
    } finally {
      loading = false;
    }
  }

  // ---- storage cleanup (prune) ----
  // Per engine: the default engine is preselected; the picker appears when more
  // than one is enabled. Each engine reports which categories it can prune.
  type DiskRow = { type: string; total: number; active: number; size: string; reclaimable: string; rawReclaimable: number; note?: string };
  type Caps = { containers: boolean; images: boolean; allImages: boolean; volumes: boolean; networks: boolean; build: boolean };
  let df: DiskRow[] = [];
  let caps: Caps = { containers: false, images: false, allImages: false, volumes: false, networks: false, build: false };
  let pruneEngines: string[] = [];
  let pruneEngine = '';
  let pruneOpts = { containers: true, images: true, allImages: false, volumes: false, networks: false, build: false };
  let pruning = false;
  let confirmPrune = false;
  let dfLoading = false;
  async function loadDf() {
    dfLoading = true;
    try {
      const r = await fetch('/api/maintenance/df' + (pruneEngine ? `?engine=${encodeURIComponent(pruneEngine)}` : ''));
      if (r.ok) {
        const d = await r.json();
        df = d.rows || [];
        caps = d.capabilities || caps;
        if (!pruneEngine) pruneEngine = d.engine || '';
      }
    } catch {}
    dfLoading = false;
  }
  async function loadPruneEngines() {
    try {
      const r = await fetch('/api/engine');
      if (r.ok) pruneEngines = ((await r.json()).engines || []).filter((e: any) => e.enabled).map((e: any) => e.name);
    } catch {}
  }
  async function pickPruneEngine(name: string) {
    pruneEngine = name;
    confirmPrune = false;
    await loadDf();
  }
  // Build leftovers go through `system prune --build`, which also takes stopped
  // containers, dangling images and unused networks; reflect that in the boxes.
  $: if (pruneOpts.build) pruneOpts.containers = pruneOpts.images = pruneOpts.networks = true;
  async function runPrune() {
    confirmPrune = false;
    pruning = true;
    try {
      const r = await fetch('/api/maintenance/prune', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ engine: pruneEngine, ...pruneOpts }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      const rep = await r.json();
      toast(rep.reclaimed ? `Reclaimed ${rep.reclaimed}` : 'Cleanup complete', { kind: 'success' });
      await loadDf();
    } catch (e: any) {
      toast(e.message || 'Cleanup failed', { kind: 'error' });
    } finally {
      pruning = false;
    }
  }
  $: anyPrune =
    (caps.containers && pruneOpts.containers) || (caps.images && pruneOpts.images) || (caps.allImages && pruneOpts.allImages) ||
    (caps.volumes && pruneOpts.volumes) || (caps.networks && pruneOpts.networks) || (caps.build && pruneOpts.build);

  // ---- left-over app data ----
  // Folders in the app-data locations that no stack and no container uses:
  // what deleting a stack left behind. Removed one at a time, two clicks each.
  type Leftovers = { folders: { path: string; bytes: number; more?: boolean }[]; unchecked?: string[] };
  let leftovers: Leftovers | null = null;
  let leftoversError = '';
  let confirmLeftover = '';
  let removingLeftover = '';
  async function loadLeftovers() {
    leftoversError = '';
    try {
      const r = await fetch('/api/maintenance/leftovers');
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      leftovers = await r.json();
    } catch (e: any) {
      leftoversError = e.message;
    }
  }
  async function removeLeftover(path: string) {
    confirmLeftover = '';
    removingLeftover = path;
    try {
      const r = await fetch('/api/maintenance/leftovers', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      toast(`Removed ${path}`, { kind: 'success' });
    } catch (e: any) {
      toast(`Could not remove ${path}: ${e.message}`, { kind: 'error' });
    } finally {
      removingLeftover = '';
      loadLeftovers();
    }
  }
  const fmtBytes = (n: number) =>
    n < 1024 ? `${n} B` : n < 1024 ** 2 ? `${(n / 1024).toFixed(0)} KB` : n < 1024 ** 3 ? `${(n / 1024 ** 2).toFixed(1)} MB` : `${(n / 1024 ** 3).toFixed(1)} GB`;
  $: leftoverTotal = (leftovers?.folders ?? []).reduce((a, f) => a + f.bytes, 0);

  onMount(() => {
    load();
    loadDf();
    loadPruneEngines();
    loadLeftovers();
  });

  const STATUS = {
    ok: { icon: 'check', cls: 'text-fjord-success', label: 'OK' },
    warn: { icon: 'alert', cls: 'text-fjord-warning', label: 'Warning' },
    fail: { icon: 'alert', cls: 'text-fjord-danger', label: 'Failed' },
    unknown: { icon: 'alert', cls: 'text-fjord-fg-dim', label: 'Unverified' },
  } as const;

  $: failCount = report?.checks.filter((c) => c.status === 'fail').length ?? 0;

  function copyFix(fix: string) {
    navigator.clipboard.writeText(fix);
    toast('Command copied', { kind: 'success' });
  }
</script>

<div class="flex flex-col h-full">
  <div class="flex items-center justify-between mb-4 shrink-0">
    <div>
      <h2 class="text-2xl font-bold text-fjord-fg">System</h2>
      <p class="text-sm text-fjord-fg-dim">Host readiness — platform, deployment mode, and engine requirements.</p>
    </div>
    <button
      on:click={load}
      class="flex items-center gap-2 bg-fjord-card hover:bg-fjord-border border border-fjord-border text-fjord-fg-secondary font-medium py-2 px-4 rounded-lg text-sm"
      ><Icon name="refresh" size={14} /> Re-check</button
    >
  </div>

  <div class="flex-1 overflow-y-auto">
    {#if loading}
      <div class="flex items-center gap-3 text-fjord-fg-dim text-sm"><Spinner size={18} /> Running checks…</div>
    {:else if error}
      <EmptyState icon="alert" title="System Report Unavailable" description={error} />
    {:else if report}
      <div class="flex items-center gap-2 mb-4 flex-wrap">
        {#if daemonVersion}
          <span class="text-[11px] font-semibold px-2.5 py-1 rounded-full bg-fjord-accent/15 text-fjord-accent border border-fjord-accent/40" title="fjordd daemon version">fjord {daemonVersion}</span>
        {/if}
        <span class="text-[11px] font-semibold uppercase tracking-wide px-2.5 py-1 rounded-full bg-fjord-card border border-fjord-border text-fjord-fg-secondary">{report.os}</span>
        <span
          class="text-[11px] font-semibold uppercase tracking-wide px-2.5 py-1 rounded-full border {report.mode === 'host'
            ? 'bg-fjord-accent/15 text-fjord-accent border-fjord-accent/40'
            : 'bg-fjord-card text-fjord-fg-muted border-fjord-border'}"
          title={report.mode === 'host'
            ? 'fjordd can see and repair the host directly'
            : 'fjordd cannot see or repair the host from here — fixes are commands for the operator'}
          >{report.mode === 'host'
            ? 'running directly on the host'
            : report.mode === 'container'
              ? 'running in a container'
              : 'deployment mode unknown'}</span
        >
        {#if engineName}
          <span class="text-[11px] font-semibold uppercase tracking-wide px-2.5 py-1 rounded-full bg-fjord-card border border-fjord-border text-fjord-fg-secondary">default engine: {engineName}</span>
        {/if}
        {#if failCount > 0}
          <span class="text-[11px] font-semibold px-2.5 py-1 rounded-full bg-fjord-danger/15 text-fjord-danger border border-fjord-danger/40"
            >{failCount} check{failCount === 1 ? '' : 's'} failing</span
          >
        {:else}
          <span class="text-[11px] font-semibold px-2.5 py-1 rounded-full bg-fjord-success/10 text-fjord-success border border-fjord-success/30">all checks passing</span>
        {/if}
      </div>

      <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border">
        {#each report.checks as c (c.id)}
          <div class="px-4 py-3">
            <div class="flex items-center gap-3">
              <div class="shrink-0 {STATUS[c.status].cls}" title={STATUS[c.status].label}>
                <Icon name={STATUS[c.status].icon} size={18} />
              </div>
              <div class="min-w-0 flex-1">
                <span class="text-sm font-medium text-fjord-fg">{c.name}</span>
                {#if c.detail}
                  <div class="text-xs text-fjord-fg-dim font-mono truncate" title={c.detail}>{c.detail}</div>
                {/if}
                {#if c.why}
                  <div class="text-xs text-fjord-fg-dim mt-0.5">{c.why}</div>
                {/if}
              </div>
              {#if c.installable}<InstallCheckButton id={c.id} name={c.name} on:installed={load} />{/if}
              <span class="shrink-0 text-[11px] font-semibold uppercase tracking-wide {STATUS[c.status].cls}">{STATUS[c.status].label}</span>
            </div>
            {#if c.fix}
              <div class="mt-2 ml-[30px]"><FixSnippet fix={c.fix} /></div>
            {/if}
          </div>
        {/each}
      </div>

      <!-- Storage cleanup (prune) -->
      <h3 class="text-lg font-bold text-fjord-fg mt-8 mb-1">Storage cleanup</h3>
      <p class="text-sm text-fjord-fg-dim mb-3">
        Reclaim disk held by resources no stack uses. What can be cleaned depends on the engine.
      </p>
      {#if pruneEngines.length > 1}
        <div class="flex items-center gap-1 mb-4 max-w-2xl">
          {#each pruneEngines as e}
            <button
              on:click={() => pickPruneEngine(e)}
              class="px-3 py-1.5 rounded-lg text-sm font-medium transition-colors {pruneEngine === e
                ? 'bg-fjord-accent text-white'
                : 'bg-fjord-card border border-fjord-border text-fjord-fg-secondary hover:text-fjord-fg'}">{e}{#if e === engineName}<span class="ml-1.5 text-[10px] uppercase tracking-wide opacity-70">default</span>{/if}</button>
          {/each}
        </div>
      {:else if pruneEngine}
        <div class="text-xs text-fjord-fg-dim mb-3">Engine: <b class="text-fjord-fg-muted">{pruneEngine}</b></div>
      {/if}

      {#if dfLoading && !df.length}
        <div class="flex items-center gap-2 text-fjord-fg-dim text-sm mb-4"><Spinner size={14} /> Measuring…</div>
      {:else if df.length}
        <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border mb-4 max-w-2xl transition-opacity {dfLoading ? 'opacity-50' : ''}">
          {#each df as row}
            <!-- One item per row so divide-y draws a single divider between
                 rows; the note lives inside the row, not as a sibling. -->
            <div class="px-4 py-2.5">
              <div class="flex items-center gap-3 text-sm">
                <span class="w-32 shrink-0 text-fjord-fg-secondary">{row.type}</span>
                <span class="text-xs text-fjord-fg-dim w-28 shrink-0">{row.active}/{row.total} in use</span>
                <span class="text-xs text-fjord-fg-dim flex-1 truncate">{row.size}</span>
                <span class="shrink-0 text-xs font-semibold {row.rawReclaimable > 0 ? 'text-fjord-warning' : 'text-fjord-fg-faint'}"
                  >{row.reclaimable} reclaimable</span
                >
              </div>
              {#if row.note}
                <div class="mt-1.5 text-xs text-fjord-fg-dim leading-snug">{row.note}</div>
              {/if}
            </div>
          {/each}
        </div>
      {/if}

      <div class="flex flex-wrap items-center gap-x-5 gap-y-2 mb-3 max-w-2xl">
        {#if caps.containers}
        <label class="flex items-center gap-1.5 text-sm text-fjord-fg-secondary cursor-pointer select-none">
          <input type="checkbox" bind:checked={pruneOpts.containers} disabled={pruneOpts.build} class="accent-fjord-accent" /> Stopped containers
        </label>
        {/if}
        {#if caps.images}
        <label class="flex items-center gap-1.5 text-sm text-fjord-fg-secondary cursor-pointer select-none">
          <input type="checkbox" bind:checked={pruneOpts.images} disabled={pruneOpts.build} class="accent-fjord-accent" /> Dangling images
        </label>
        {/if}
        {#if caps.networks}
        <label class="flex items-center gap-1.5 text-sm text-fjord-fg-secondary cursor-pointer select-none" title="Networks no container is attached to">
          <input type="checkbox" bind:checked={pruneOpts.networks} disabled={pruneOpts.build} class="accent-fjord-accent" /> Unused networks
        </label>
        {/if}
        {#if caps.build}
        <label class="flex items-center gap-1.5 text-sm text-fjord-fg-secondary cursor-pointer select-none" title="Working containers and cache left behind by image builds. Includes stopped containers, dangling images and unused networks.">
          <input type="checkbox" bind:checked={pruneOpts.build} class="accent-fjord-accent" /> Build leftovers
        </label>
        {/if}
        {#if caps.allImages}
        <label class="flex items-center gap-1.5 text-sm text-fjord-fg-secondary cursor-pointer select-none" title="Removes every image not used by a container — they re-pull when next needed">
          <input type="checkbox" bind:checked={pruneOpts.allImages} class="accent-fjord-accent" /> All unused images
        </label>
        {/if}
        {#if caps.volumes}
        <label class="flex items-center gap-1.5 text-sm cursor-pointer select-none {pruneOpts.volumes ? 'text-fjord-warning' : 'text-fjord-fg-secondary'}" title="Deletes volumes not attached to any container — this can destroy data">
          <input type="checkbox" bind:checked={pruneOpts.volumes} class="accent-fjord-accent" /> Unused volumes
        </label>
        {/if}
      </div>
      {#if caps.volumes && pruneOpts.volumes}
        <p class="text-xs text-fjord-warning mb-3 max-w-2xl">⚠ Unused volumes may hold real data — anything not currently mounted by a container will be deleted.</p>
      {/if}

      {#if confirmPrune}
        <div class="flex items-center gap-3">
          <span class="text-sm text-fjord-fg-secondary">Remove the selected unused resources now?</span>
          <button on:click={runPrune} class="px-4 py-2 rounded-lg text-sm font-medium bg-fjord-danger hover:bg-fjord-danger-hover text-white">Confirm cleanup</button>
          <button on:click={() => (confirmPrune = false)} class="px-4 py-2 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
        </div>
      {:else}
        <button
          on:click={() => (confirmPrune = true)}
          disabled={pruning || !anyPrune}
          class="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-fjord-border hover:bg-fjord-danger hover:text-white transition-colors disabled:opacity-40"
        >
          {#if pruning}<Spinner size={14} /> Cleaning…{:else}<Icon name="trash" size={14} /> Clean up{/if}
        </button>
      {/if}

      <!-- Left-over app data -->
      <h3 class="text-lg font-bold text-fjord-fg mt-8 mb-1">Left-over app data</h3>
      <p class="text-sm text-fjord-fg-dim mb-3 max-w-2xl">
        Folders in your app-data locations that no stack and no container uses — usually what a deleted stack left
        behind.
      </p>
      {#if leftoversError}
        <p class="text-sm text-fjord-danger max-w-2xl">Could not check: {leftoversError}</p>
      {:else if !leftovers}
        <div class="flex items-center gap-2 text-fjord-fg-dim text-sm"><Spinner size={14} /> Looking…</div>
      {:else if !leftovers.folders.length}
        <p class="text-sm text-fjord-fg-dim">None — every folder there is in use.</p>
      {:else}
        <div class="text-xs text-fjord-fg-dim mb-2">
          {leftovers.folders.length} folder{leftovers.folders.length === 1 ? '' : 's'}, {fmtBytes(leftoverTotal)}
        </div>
        <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border mb-2 max-w-2xl">
          {#each leftovers.folders as f (f.path)}
            <div class="flex items-center gap-3 px-4 py-2.5 text-sm">
              <span class="flex-1 font-mono text-fjord-fg-body truncate" title={f.path}>{f.path}</span>
              <span class="shrink-0 text-xs text-fjord-fg-dim">{f.more ? 'more than ' : ''}{fmtBytes(f.bytes)}</span>
              {#if confirmLeftover === f.path}
                <button
                  on:click={() => removeLeftover(f.path)}
                  class="shrink-0 px-3 py-1 rounded-lg text-xs font-medium bg-fjord-danger hover:bg-fjord-danger-hover text-white"
                  >Delete for good</button
                >
                <button on:click={() => (confirmLeftover = '')} class="shrink-0 text-xs text-fjord-fg-muted hover:text-fjord-fg"
                  >Cancel</button
                >
              {:else}
                <button
                  on:click={() => (confirmLeftover = f.path)}
                  disabled={removingLeftover === f.path}
                  title="Remove this folder"
                  class="shrink-0 p-1.5 rounded-lg text-fjord-fg-muted hover:bg-fjord-danger hover:text-white transition-colors disabled:opacity-40"
                  >{#if removingLeftover === f.path}<Spinner size={14} />{:else}<Icon name="trash" size={14} />{/if}</button
                >
              {/if}
            </div>
          {/each}
        </div>
        {#if leftovers.unchecked?.length}
          <p class="text-xs text-fjord-warning max-w-2xl">
            Not checked against {leftovers.unchecked.join(', ')}: it cannot list what its containers mount, so make sure
            none of them uses a folder before removing it.
          </p>
        {/if}
      {/if}
    {/if}
  </div>
</div>
