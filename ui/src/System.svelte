<script lang="ts">
  import { isoDate, isoDateTime } from './when';
  import { installAgain, type Leftover } from './keptData';
  import { createEventDispatcher, onMount } from 'svelte';
  import NetworkBackend from './NetworkBackend.svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import HistoryList from './HistoryList.svelte';
  import EmptyState from './EmptyState.svelte';
  import InstallCheckButton from './InstallCheckButton.svelte';
  import FixSnippet from './FixSnippet.svelte';
  import { toast } from './toast';
  import { browserZone } from './setupPlan';

  type CheckResult = {
    id: string;
    name: string;
    status: 'ok' | 'warn' | 'fail' | 'unknown';
    detail?: string;
    why?: string;
    fix?: string;
    installable?: boolean;
    action?: string;
  };
  type Report = {
    os: string;
    mode: string;
    canInstall: boolean;
    checks: CheckResult[];
    muted?: string[];
  };

  export let tab: 'checks' | 'cleanup' | 'history' = 'checks';
  const dispatch = createEventDispatcher<{ tab: 'checks' | 'cleanup' | 'history' }>();
  let activeTab: 'checks' | 'cleanup' | 'history' = tab;
  $: activeTab = tab;
  function selectTab(t: 'checks' | 'cleanup' | 'history') {
    activeTab = t;
    dispatch('tab', t);
  }

  let report: Report | null = null;
  let engineName = '';
  let netBackend: { backend?: string; note?: string } | null = null;
  let loading = true;
  let error = '';

  async function unmute(id: string) {
    const res = await fetch('/api/conditions/mute', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id, muted: false }),
    }).catch(() => null);
    if (!res?.ok) {
      toast('Could not turn the banner back on', { kind: 'error' });
      return;
    }
    if (report) report = { ...report, muted: (report.muted ?? []).filter((m) => m !== id) };
  }

  async function load() {
    loading = true;
    error = '';
    try {
      const [setupRes, netRes] = await Promise.all([
        fetch(`/api/setup?tz=${encodeURIComponent(browserZone())}`),
        fetch('/api/system/netavark').catch(() => null),
      ]);
      if (!setupRes.ok) throw new Error(await setupRes.text());
      report = await setupRes.json();
      netBackend = netRes?.ok ? await netRes.json() : null;
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
      if (r.ok) {
        const d = await r.json();
        engineName = d.default || '';
        pruneEngines = (d.engines || []).filter((e: any) => e.enabled).map((e: any) => e.name);
      }
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
  type Leftovers = { folders: Leftover[]; unchecked?: string[] };
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
  // The App Store opens its install wizard for the app, under the name the
  // data is filed under, and the wizard finds the data there.
  function installOnKept(f: Leftover) {
    if (!f.appId) return;
    installAgain.set({ appId: f.appId, name: f.name || f.appId, catalog: f.catalog });
    location.hash = '#/store';
  }
  const fmtBytes = (n: number) =>
    n < 1024 ? `${n} B` : n < 1024 ** 2 ? `${(n / 1024).toFixed(0)} KB` : n < 1024 ** 3 ? `${(n / 1024 ** 2).toFixed(1)} MB` : `${(n / 1024 ** 3).toFixed(1)} GB`;
  $: leftoverTotal = (leftovers?.folders ?? []).reduce((a, f) => a + f.bytes, 0);

  // What fjord changed on the host with nobody asking (for example when
  // podman moved to another network backend), shown until dismissed.
  type HostNotice = { id: string; at: string; title: string; lines: string[] };
  let notices: HostNotice[] = [];
  async function loadNotices() {
    try {
      const r = await fetch('/api/host-notices');
      notices = r.ok ? ((await r.json()) ?? []) : [];
    } catch {
      notices = [];
    }
  }
  async function dismissNotice(id: string) {
    await fetch(`/api/host-notices/${encodeURIComponent(id)}`, { method: 'DELETE' });
    notices = notices.filter((n) => n.id !== id);
  }

  onMount(() => {
    load();
    loadNotices();
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

  // Passing checks fold into one row; only the ones that need the person
  // are listed in full.
  $: attention = report?.checks.filter((c) => c.status !== 'ok') ?? [];
  $: passing = report?.checks.filter((c) => c.status === 'ok') ?? [];
  let showPassing = false;
  // The passing check whose explanation is open; one at a time.
  let openWhy = '';

  function copyFix(fix: string) {
    navigator.clipboard.writeText(fix);
    toast('Command copied', { kind: 'success' });
  }
</script>

<div class="flex flex-col h-full">
  <div class="flex items-center justify-between mb-4 shrink-0">
    <div>
      <h2 class="text-2xl font-bold text-fjord-fg">System</h2>
      <p class="text-sm text-fjord-fg-dim">Host readiness, disk cleanup, and activity across all apps.</p>
    </div>
    {#if activeTab === 'checks'}
      <button
        on:click={load}
        class="flex items-center gap-2 bg-fjord-card hover:bg-fjord-border border border-fjord-border text-fjord-fg-secondary font-medium py-2 px-4 rounded-lg text-sm"
        ><Icon name="refresh" size={14} /> Re-check</button
      >
    {:else if activeTab === 'cleanup'}
      <button
        on:click={() => {
          loadDf();
          loadLeftovers();
        }}
        class="flex items-center gap-2 bg-fjord-card hover:bg-fjord-border border border-fjord-border text-fjord-fg-secondary font-medium py-2 px-4 rounded-lg text-sm"
        ><Icon name="refresh" size={14} /> Refresh</button
      >
    {/if}
  </div>

  <div class="flex-1 overflow-y-auto">
    {#each notices as n (n.id)}
      <div class="mb-4 rounded-lg border border-fjord-accent/40 bg-fjord-accent/10 p-4" data-testid="host-notice">
        <div class="flex items-start justify-between gap-4">
          <div>
            <p class="text-sm font-semibold text-fjord-fg">{n.title}</p>
            <p class="text-xs text-fjord-fg-dim">{isoDateTime(n.at)}</p>
          </div>
          <button
            on:click={() => dismissNotice(n.id)}
            class="shrink-0 text-xs font-medium text-fjord-fg-secondary border border-fjord-border rounded-md px-2 py-1 hover:bg-fjord-border"
            >Dismiss</button
          >
        </div>
        <ul class="mt-2 list-disc pl-5 text-sm text-fjord-fg-secondary space-y-1">
          {#each n.lines as line}
            <li>{line}</li>
          {/each}
        </ul>
      </div>
    {/each}
    <div role="tablist" aria-label="System sections" class="flex gap-1 mb-5 border-b border-fjord-border max-w-3xl">
      <button
        type="button"
        role="tab"
        aria-selected={activeTab === 'checks'}
        on:click={() => selectTab('checks')}
        class="px-4 py-2 text-sm font-medium -mb-px border-b-2 transition-colors flex items-center gap-1.5 {activeTab === 'checks'
          ? 'border-fjord-accent text-fjord-fg'
          : 'border-transparent text-fjord-fg-muted hover:text-fjord-fg-body'}"
      >
        Checks
        {#if attention.length > 0}
          <span class="rounded-full bg-fjord-warning/20 text-fjord-warning px-1.5 py-0.25 text-[11px] font-semibold">{attention.length}</span>
        {/if}
      </button>
      <button
        type="button"
        role="tab"
        aria-selected={activeTab === 'cleanup'}
        on:click={() => selectTab('cleanup')}
        class="px-4 py-2 text-sm font-medium -mb-px border-b-2 transition-colors flex items-center gap-1.5 {activeTab === 'cleanup'
          ? 'border-fjord-accent text-fjord-fg'
          : 'border-transparent text-fjord-fg-muted hover:text-fjord-fg-body'}"
      >
        Cleanup
        {#if (leftovers?.folders?.length ?? 0) > 0}
          <span class="rounded-full border border-fjord-border bg-fjord-inset text-fjord-fg-muted px-1.5 py-0.25 text-[11px] font-medium">{leftovers?.folders?.length}</span>
        {/if}
      </button>
      <button
        type="button"
        role="tab"
        aria-selected={activeTab === 'history'}
        on:click={() => selectTab('history')}
        class="px-4 py-2 text-sm font-medium -mb-px border-b-2 transition-colors {activeTab === 'history'
          ? 'border-fjord-accent text-fjord-fg'
          : 'border-transparent text-fjord-fg-muted hover:text-fjord-fg-body'}">History</button
      >
    </div>

    {#if activeTab === 'checks'}
    {#if loading}
      <div class="flex items-center gap-3 text-fjord-fg-dim text-sm"><Spinner size={18} /> Running checks…</div>
    {:else if error}
      <EmptyState icon="alert" title="System Report Unavailable" description={error} />
    {:else if report}
      {#if report.mode === 'container'}
        <p class="text-xs text-fjord-fg-muted mb-3 max-w-3xl">
          Running in a container. Fixes below are commands to run on the host.
        </p>
      {/if}
      <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border max-w-3xl">
        {#each attention as c (c.id)}
          <div class="px-4 py-3" data-check={c.id}>
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
                  <div class="text-xs text-fjord-fg-dim mt-0.5 max-w-prose">{c.why}</div>
                {/if}
              </div>
              {#if report?.muted?.includes(c.id)}
                <button
                  type="button"
                  data-unmute={c.id}
                  on:click={() => unmute(c.id)}
                  class="shrink-0 px-2 py-1 rounded-md text-xs text-fjord-fg-secondary hover:text-fjord-fg border border-fjord-border hover:border-fjord-neutral"
                  title="Its banner is turned off">Show its banner again</button
                >
              {/if}
              {#if c.installable}<InstallCheckButton id={c.id} name={c.name} label={c.action && !c.action.startsWith('Install') ? c.action : 'Install'} busyLabel={c.action && !c.action.startsWith('Install') ? 'Working…' : 'Installing…'} on:installed={load} />{/if}
              <span class="shrink-0 text-[11px] font-semibold uppercase tracking-wide {STATUS[c.status].cls}">{STATUS[c.status].label}</span>
            </div>
            {#if c.fix}
              <div class="mt-2 ml-[30px]"><FixSnippet fix={c.fix} /></div>
            {/if}
          </div>
        {/each}
        {#if passing.length}
          <button
            type="button"
            data-checks-passing
            aria-expanded={showPassing}
            on:click={() => (showPassing = !showPassing)}
            class="w-full flex items-center gap-3 px-4 py-3 text-left hover:bg-fjord-card"
          >
            <span class="shrink-0 text-fjord-success"><Icon name="check" size={18} /></span>
            <span class="flex-1 text-sm font-medium text-fjord-fg"
              >{#if attention.length}{passing.length} other check{passing.length === 1 ? '' : 's'} passing{:else}All {passing.length} checks passing{/if}</span
            >
            <span class="shrink-0 text-fjord-fg-dim"><Icon name={showPassing ? 'chevron-up' : 'chevron-down'} size={16} /></span>
          </button>
          {#if showPassing}
            {#each passing as c (c.id)}
              <div data-check={c.id}>
                <button
                  type="button"
                  disabled={!c.why}
                  aria-expanded={c.why ? openWhy === c.id : undefined}
                  on:click={() => (openWhy = openWhy === c.id ? '' : c.id)}
                  class="w-full flex flex-col sm:flex-row sm:items-baseline gap-x-3 px-4 py-2 pl-[46px] text-left enabled:hover:bg-fjord-card"
                >
                  <span class="sm:shrink-0 text-sm text-fjord-fg-secondary">{c.name}</span>
                  {#if c.detail}
                    <span class="min-w-0 sm:flex-1 sm:text-right text-xs text-fjord-fg-dim font-mono truncate">{c.detail}</span>
                  {/if}
                </button>
                {#if c.why && openWhy === c.id}
                  <p class="px-4 pb-3 pl-[46px] text-xs text-fjord-fg-dim max-w-prose" data-check-why>{c.why}</p>
                {/if}
              </div>
            {/each}
            {#if netBackend?.backend}
              <div data-check="network-backend">
                <button
                  type="button"
                  disabled={!netBackend.note}
                  aria-expanded={netBackend.note ? openWhy === 'network-backend' : undefined}
                  on:click={() => (openWhy = openWhy === 'network-backend' ? '' : 'network-backend')}
                  class="w-full flex flex-col sm:flex-row sm:items-baseline gap-x-3 px-4 py-2 pl-[46px] text-left enabled:hover:bg-fjord-card"
                >
                  <span class="sm:shrink-0 text-sm text-fjord-fg-secondary">Network backend</span>
                  <span class="min-w-0 sm:flex-1 sm:text-right text-xs text-fjord-fg-dim font-mono truncate">detected {netBackend.backend === 'cni' ? 'CNI' : netBackend.backend}</span>
                </button>
                {#if netBackend.note && openWhy === 'network-backend'}
                  <p class="px-4 pb-3 pl-[46px] text-xs text-fjord-fg-dim max-w-prose" data-check-why>{netBackend.note}</p>
                {/if}
              </div>
            {/if}
          {/if}
        {/if}
      </div>

      <NetworkBackend />
    {/if}
    {/if}

    {#if activeTab === 'cleanup'}
      <!-- Storage cleanup (prune) -->
      <h3 class="text-sm font-semibold text-fjord-fg-secondary uppercase tracking-wide mb-1">Storage cleanup</h3>
      <p class="text-xs text-fjord-fg-dim mb-3 max-w-2xl">
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
      <h3 class="text-sm font-semibold text-fjord-fg-secondary uppercase tracking-wide mt-8 mb-1">Left-over app data</h3>
      <p class="text-xs text-fjord-fg-dim mb-3 max-w-2xl">
        {#if report?.checks?.some((c) => c.id === 'zfs' && c.status === 'ok') || leftovers?.folders?.some((f) => f.dataset)}
          Folders and ZFS datasets in your app-data locations that no stack and no container uses.
        {:else}
          Folders in your app-data locations that no stack and no container uses.
        {/if}
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
            <div class="flex items-center gap-3 px-4 py-2.5 text-sm" data-leftover={f.path}>
              <div class="flex-1 min-w-0">
                <div class="font-mono text-fjord-fg-body truncate" title={f.path}>{f.path}</div>
                {#if f.setAside}
                  <div class="text-xs text-fjord-fg-dim mt-0.5">
                    Set aside when {f.name} was installed fresh{f.until ? `; fjord removes it on ${isoDate(f.until)}` : ''}.
                  </div>
                {:else if f.deletedAt}
                  <div class="text-xs text-fjord-fg-dim mt-0.5">
                    Deleted {isoDate(f.deletedAt)}{f.version ? `, last used by ${f.version}` : ''}
                  </div>
                {/if}
                {#if confirmLeftover === f.path && f.dataset && (f.restorePoints || f.keptCopies)}
                  <div class="text-xs text-fjord-danger mt-0.5">
                    {#if f.restorePoints && f.keptCopies}
                      Its {f.restorePoints} restore {f.restorePoints === 1 ? 'point' : 'points'} and {f.keptCopies} kept {f.keptCopies === 1 ? 'copy' : 'copies'} are removed with it.
                    {:else if f.restorePoints}
                      Its {f.restorePoints} restore {f.restorePoints === 1 ? 'point' : 'points'} {f.restorePoints === 1 ? 'is' : 'are'} removed with it.
                    {:else}
                      Its {f.keptCopies} kept {f.keptCopies === 1 ? 'copy' : 'copies'} {f.keptCopies === 1 ? 'is' : 'are'} removed with it.
                    {/if}
                  </div>
                {/if}
              </div>
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
                {#if f.installable && !f.setAside}
                  <button
                    on:click={() => installOnKept(f)}
                    title="Install {f.appId} again on this data"
                    class="shrink-0 px-3 py-1 rounded-lg text-xs font-medium border border-fjord-border text-fjord-fg-secondary hover:bg-fjord-border/40 hover:text-fjord-fg transition-colors"
                    >Install again</button
                  >
                {/if}
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

      {#if activeTab === 'history'}
      <!-- History: every stack, deleted ones too -->
      <p class="text-xs text-fjord-fg-dim mb-3 max-w-2xl">
        What happened across all apps, newest first. Each app's own History tab has the rest.
      </p>
      <div class="max-w-3xl" data-host-history><HistoryList limit={50} /></div>
      {/if}
  </div>
</div>
