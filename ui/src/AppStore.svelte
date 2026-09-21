<script lang="ts">
  import { onMount, createEventDispatcher } from 'svelte';
  import InstallWizard from './InstallWizard.svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import EmptyState from './EmptyState.svelte';
  import { toast } from './toast';

  const dispatch = createEventDispatcher();

  type AppVariant = {
    id: string;
    label: string;
    default?: boolean;
    image: string;
    version?: string;
  };

  type CatalogApp = {
    id: string;
    name: string;
    category: string;
    class: string;
    icon: string;
    description?: string;
    upstream_url?: string;
    web_url?: string;
    image?: string;
    manifest_url: string;
    catalog?: string;
    catalog_name?: string;
    version: string;
    variants: AppVariant[];
    catalog_icon?: string;
    architectures?: string[]; // OCI names the image is built for; absent = no restriction
    sources?: AppSource[];
  };

  type AppSource = {
    catalog: string;
    catalog_name: string;
    catalog_icon?: string;
    manifest_url: string;
    icon?: string;
    image?: string;
    version: string;
    variants: AppVariant[];
  };

  type Catalog = {
    catalog_name: string;
    apps: CatalogApp[];
  };

  let catalog: Catalog | null = null;
  let loading = true;
  let error: string | null = null;
  let installingApp: CatalogApp | null = null;
  // The wizard stays mounted while the daemon decides. A refusal it can do
  // something about -- a bad address, a name in use -- has to land back in the
  // form that produced it, not after it has been thrown away.
  let installBusy = false;
  let installError = '';
  let detailApp: CatalogApp | null = null; // app-detail view (click a card)
  let search = '';
  let selectedCategory = 'All';

  // Origin badges only matter once more than one catalog contributes apps.
  $: multiCatalog = new Set((catalog?.apps ?? []).map((a: any) => a.catalog_name)).size > 1;

  $: categories = catalog
    ? ['All', ...Array.from(new Set(catalog.apps.map((a) => a.category).filter(Boolean))).sort()]
    : ['All'];
  $: filtered = catalog
    ? catalog.apps.filter((a) => {
        const inCat = selectedCategory === 'All' || a.category === selectedCategory;
        const q = search.trim().toLowerCase();
        const inSearch = !q || a.name.toLowerCase().includes(q) || (a.category || '').toLowerCase().includes(q);
        return inCat && inSearch && (showUnsupported || supported(a));
      })
    : [];
  // Apps that match the search/category but aren't built for this host.
  $: hiddenUnsupported = catalog && !showUnsupported
    ? catalog.apps.filter((a) => {
        const inCat = selectedCategory === 'All' || a.category === selectedCategory;
        const q = search.trim().toLowerCase();
        const inSearch = !q || a.name.toLowerCase().includes(q) || (a.category || '').toLowerCase().includes(q);
        return inCat && inSearch && !supported(a);
      }).length
    : 0;

  // Some upstream versions already carry a leading "v"; don't double it.
  // No version (or a placeholder like "unknown") -> render nothing, not "v".
  const ver = (v?: string) => (!v || v === 'unknown' ? '' : 'v' + v.replace(/^v/, ''));

  // 404 is not an error: it means the cache dir has no catalog yet (fresh
  // install) -- rendered as guidance, not "Catalog Unavailable".
  let missing = false;
  async function loadCatalog() {
    error = '';
    missing = false;
    try {
      // cache:no-store defeats any stale copy the browser cached back when the
      // daemon served catalog.json as cacheable -- otherwise a reload keeps
      // showing an old app list after catalogs change.
      const res = await fetch('/catalog/catalog.json', { cache: 'no-store' });
      if (res.status === 404) {
        missing = true;
        return;
      }
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const doc = await res.json();
      doc.apps ??= [];
      catalog = doc;
      missing = doc.apps.length === 0; // merged doc is 200 even with no sources
    } catch (e: any) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  // With no catalogs configured, refresh has nothing to pull from -- show
  // that on the button instead of a failing call. Fetched BEFORE the catalog
  // so the empty state can tell "none configured" apart from "not fetched
  // yet". sourceCount counts refreshable sources (the local seed needs none).
  let sourceCount = 0;
  // Host CPU architecture (OCI spelling). Apps whose images aren't built for
  // it are hidden by default: an arm64 host showing 75 amd64-only apps is a
  // store full of things that can't run.
  let hostArch = '';
  let showUnsupported = false;
  const supported = (a: any) => !hostArch || !Array.isArray(a.architectures) || a.architectures.length === 0 || a.architectures.includes(hostArch);
  onMount(async () => {
    try {
      const res = await fetch('/api/about');
      if (res.ok) {
        const about = await res.json();
        sourceCount = about.catalogs || 0;
        hostArch = about.arch || '';
      }
    } catch {
      // about unavailable -> leave the button enabled; refresh reports errors
    }
    await loadCatalog();
  });

  let refreshing = false;
  async function refreshCatalog() {
    refreshing = true;
    try {
      const res = await fetch('/api/catalog/refresh', { method: 'POST' });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      await loadCatalog();
      toast('Catalog refreshed', { kind: 'success' });
    } catch (e: any) {
      toast(e.message || 'Catalog refresh failed', { kind: 'error' });
    } finally {
      refreshing = false;
    }
  }

  // Inline placeholder: no third-party request (works offline, leaks nothing),
  // and it can't fail again and loop.
  const FALLBACK_ICON =
    'data:image/svg+xml;utf8,' +
    encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="#94a3b8" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="4"/><path d="M8 12h8M12 8v8"/></svg>');
  function handleImgError(e: Event) {
    const img = e.target as HTMLImageElement;
    if (img.src === FALLBACK_ICON) return;
    img.src = FALLBACK_ICON;
  }

  function handleDeploy(e: CustomEvent<{name: string, engine: string, manifest: string, values: Record<string,string>, paths: Record<string,string[]>, appData: string, tag: string, network: string, ip: string, mac: string, networkPlan?: Record<string,string>, networkModes?: Record<string,string>}>) {
    if (!installingApp) return;
    installBusy = true;
    installError = '';
    dispatch('install', {
        name: e.detail.name || installingApp.id,
        appId: installingApp.id,
        engine: e.detail.engine,
        manifest: e.detail.manifest,
        values: e.detail.values,
        paths: e.detail.paths,
        appData: e.detail.appData,
        tag: e.detail.tag,
        network: e.detail.network,
        ip: e.detail.ip,
        mac: e.detail.mac,
        // Taken: there is a stack now, and the install streams on its page.
        accepted: () => {
          installBusy = false;
          installingApp = null;
        },
        // Refused before anything was saved: keep the wizard and say why.
        refused: (msg: string) => {
          installBusy = false;
          installError = msg;
        },
    });
  }
</script>

<div class="h-full flex flex-col relative">
  <header class="shrink-0 mb-5">
    <div class="flex justify-between items-end gap-4 mb-4">
      <div>
        <h2 class="text-3xl font-semibold text-fjord-fg tracking-tight">App Store</h2>
        <p class="text-fjord-fg-muted mt-1">
          {#if catalog}
            {filtered.length} of {catalog.apps.length} apps
          {:else}
            Loading catalog…
          {/if}
        </p>
      </div>
      {#if catalog}
        <div class="flex items-center gap-2">
          <input
            bind:value={search}
            placeholder="Search apps…"
            class="w-64 bg-fjord-card border border-fjord-border rounded-md px-3 py-2 text-sm text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
          />
          <button
            on:click={refreshCatalog}
            disabled={refreshing || sourceCount === 0}
            title={sourceCount > 0
              ? `Re-fetch ${sourceCount === 1 ? 'the configured catalog' : `all ${sourceCount} catalogs`}`
              : 'No catalogs configured — add one in Settings'}
            class="flex items-center gap-2 px-3 py-2 rounded-md text-sm font-medium bg-fjord-card border border-fjord-border text-fjord-fg-secondary hover:text-fjord-fg hover:border-fjord-accent/40 transition-colors disabled:opacity-50"
            ><Icon name="refresh" size={13} class={refreshing ? 'animate-spin' : ''} />
            {refreshing ? 'Refreshing…' : 'Refresh'}</button
          >
        </div>
      {/if}
    </div>
    {#if catalog}
      <div class="flex flex-wrap gap-2">
        {#each categories as c}
          <button
            on:click={() => (selectedCategory = c)}
            class="px-3 py-1 rounded-full text-xs font-medium border transition-colors {selectedCategory === c
              ? 'bg-fjord-accent border-fjord-accent text-white'
              : 'bg-fjord-card border-fjord-border text-fjord-fg-muted hover:text-fjord-fg hover:border-fjord-accent/40'}"
            >{c}</button
          >
        {/each}
      </div>
    {/if}
  </header>

  {#if loading}
    <div class="flex-1 flex items-center justify-center gap-3 text-fjord-fg-muted">
      <Spinner size={22} /> Loading catalog…
    </div>
  {:else if error}
    <div class="flex-1">
      <EmptyState icon="alert" title="Catalog Unavailable" description={error} />
    </div>
  {:else if missing && sourceCount === 0}
    <div class="flex-1">
      <EmptyState
        icon="store"
        title="No Catalogs"
        description="No app catalogs are configured. Add one under Settings — any base URL publishing catalog.json (another fjord instance's /catalog works)."
      />
    </div>
  {:else if missing}
    <div class="flex-1">
      <EmptyState
        icon="store"
        title="Catalogs Not Fetched Yet"
        description="Catalogs are configured but nothing has been fetched yet."
        actionLabel="Fetch Catalogs"
        on:action={refreshCatalog}
      />
    </div>
  {:else if catalog && filtered.length === 0}
    <div class="flex-1">
      <EmptyState
        icon="search"
        title="No Results Found"
        description={hiddenUnsupported ? `${hiddenUnsupported} matching app${hiddenUnsupported === 1 ? '' : 's'} hidden — not built for ${hostArch}.` : 'Try a different search or category.'}
      />
      {#if hiddenUnsupported}
        <div class="text-center -mt-4"><button on:click={() => (showUnsupported = true)} class="text-xs text-fjord-accent hover:underline">Show them anyway</button></div>
      {/if}
    </div>
  {:else if catalog}
    {#if hiddenUnsupported || showUnsupported}
      <p class="text-xs text-fjord-fg-dim mb-3">
        {#if showUnsupported}
          Showing apps not built for <span class="font-mono">{hostArch}</span> too — they can't be installed here.
          <button on:click={() => (showUnsupported = false)} class="ml-1 text-fjord-accent hover:underline">Hide</button>
        {:else}
          {hiddenUnsupported} app{hiddenUnsupported === 1 ? '' : 's'} hidden — not built for <span class="font-mono">{hostArch}</span>.
          <button on:click={() => (showUnsupported = true)} class="ml-1 text-fjord-accent hover:underline">Show</button>
        {/if}
      </p>
    {/if}
    <!-- columns from the available width, not the viewport: the sidebar eats a third of it -->
    <div class="grid grid-cols-[repeat(auto-fill,minmax(340px,1fr))] gap-6 overflow-y-auto pb-8 pr-2">
      {#each filtered as app}
        <!-- svelte-ignore a11y-click-events-have-key-events -->
        <div class="bg-fjord-card border border-fjord-border rounded-xl p-6 shadow-xl backdrop-blur-sm hover:border-fjord-accent/50 transition-colors flex flex-col group relative cursor-pointer" on:click={() => detailApp = app}>
            <div class="flex items-start gap-4 mb-4">
                <div class="relative shrink-0">
                    <div class="w-12 h-12 rounded-lg bg-fjord-bg border border-fjord-border flex items-center justify-center overflow-hidden">
                        {#if app.icon}
                            <img src={app.icon} alt={app.name} class="w-8 h-8 object-contain" on:error={handleImgError} />
                        {:else}
                            <span class="text-xl font-bold text-fjord-fg-dim">{app.name[0]}</span>
                        {/if}
                    </div>
                    {#if app.catalog_icon}
                        <!-- catalog provenance: the source catalog's logo, corner-badged -->
                        <div
                            class="absolute -bottom-1 -right-1 w-5 h-5 rounded-md bg-fjord-card border border-fjord-border flex items-center justify-center overflow-hidden shadow"
                            title={(app.sources?.length ?? 1) > 1 ? `${app.sources?.map((s) => s.catalog_name).join(', ')} — pick one when installing` : `From ${app.catalog_name}`}
                        >
                            <img src={app.catalog_icon} alt={app.catalog_name} class="w-3.5 h-3.5 object-contain" />
                        </div>
                    {/if}
                </div>
                <div class="min-w-0 flex-1">
                    <h3 class="text-xl font-bold text-fjord-fg break-words leading-tight group-hover:text-fjord-accent transition-colors">{app.name}</h3>
                    <p class="text-xs text-fjord-accent font-medium mt-0.5">{app.category}</p>
                    {#if (app.sources?.length ?? 1) > 1}
                        <span class="inline-block mt-1 text-[10px] font-medium px-1.5 py-0.5 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-muted" title="Available from {app.sources?.map((s) => s.catalog_name).join(', ')} — pick one when installing">{app.sources?.length} catalogs</span>
                    {:else if multiCatalog && app.catalog_name}
                        <span class="inline-block mt-1 text-[10px] font-medium px-1.5 py-0.5 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-muted" title="From the {app.catalog_name} catalog">{app.catalog_name}</span>
                    {/if}
                </div>
            </div>
            {#if app.description}
                <p class="text-xs text-fjord-fg-muted leading-relaxed line-clamp-2 mb-2">{app.description}</p>
            {/if}

            <div class="mt-auto pt-4 border-t border-fjord-border/50 flex justify-between items-center gap-3">
                {#if ver(app.version)}<span class="text-xs font-mono text-fjord-fg-muted truncate min-w-0" title={ver(app.version)}>{ver(app.version)}</span>{/if}
                {#if !supported(app)}
                    <span class="text-[10px] font-medium px-1.5 py-0.5 rounded bg-fjord-warning/10 border border-fjord-warning/30 text-fjord-warning" title="Image is built for {(app.architectures || []).join(', ')} — this host is {hostArch}">{(app.architectures || []).join('/')} only</span>
                {/if}
                <button
                    on:click|stopPropagation={() => (installingApp = app)}
                    disabled={!supported(app)}
                    title={supported(app) ? '' : `Not built for ${hostArch}`}
                    class="ml-auto shrink-0 bg-fjord-border hover:bg-fjord-accent hover:text-white transition-all px-4 py-1.5 rounded-md text-sm font-medium shadow-sm disabled:opacity-40 disabled:hover:bg-fjord-border disabled:hover:text-inherit">
                    Install…
                </button>
            </div>
        </div>
      {/each}
    </div>
  {/if}

  {#if detailApp}
    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
    <div
      class="fixed inset-0 bg-black/70 backdrop-blur-sm flex items-center justify-center z-40 p-4"
      on:click|self={() => (detailApp = null)}
    >
      <div class="bg-fjord-card border border-fjord-border rounded-xl shadow-2xl w-full max-w-xl p-7">
        <div class="flex items-start gap-5">
          <div class="w-16 h-16 rounded-xl bg-fjord-bg border border-fjord-border flex items-center justify-center overflow-hidden shrink-0">
            {#if detailApp.icon}
              <img src={detailApp.icon} alt={detailApp.name} class="w-11 h-11 object-contain" on:error={handleImgError} />
            {:else}
              <span class="text-2xl font-bold text-fjord-fg-dim">{detailApp.name[0]}</span>
            {/if}
          </div>
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <h3 class="text-2xl font-bold text-fjord-fg truncate">{detailApp.name}</h3>
              {#if detailApp.class === 'stack'}
                <span class="shrink-0 text-[11px] font-medium bg-fjord-accent/20 text-fjord-accent px-1.5 py-0.5 rounded" title="Multi-service stack">Stack</span>
              {/if}
            </div>
            <p class="text-sm text-fjord-accent font-medium">{detailApp.category}{ver(detailApp.version) ? ` · ${ver(detailApp.version)}` : ''}</p>
            {#if detailApp.image}
              <p class="text-xs font-mono text-fjord-fg-dim mt-1 truncate">{detailApp.image}</p>
            {/if}
          </div>
          <button
            on:click={() => (detailApp = null)}
            title="Close"
            class="p-1.5 rounded-full text-fjord-fg-muted hover:text-fjord-fg hover:bg-fjord-border shrink-0"
            ><Icon name="close" size={16} /></button
          >
        </div>

        {#if detailApp.description}
          <p class="text-sm text-fjord-fg-secondary leading-relaxed mt-5">{detailApp.description}</p>
        {/if}

        {#if detailApp.variants?.length > 1}
          <div class="mt-5">
            <div class="text-xs font-semibold text-fjord-fg-dim mb-1.5">Builds</div>
            <div class="flex flex-wrap gap-1.5">
              {#each detailApp.variants as v}
                <span class="text-xs px-2 py-1 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-secondary">
                  {v.label}{v.version ? ` · ${v.version}` : ''}{v.default ? ' ★' : ''}
                </span>
              {/each}
            </div>
          </div>
        {/if}

        <div class="flex items-center gap-4 mt-6 pt-5 border-t border-fjord-border">
          {#if detailApp.web_url}
            <a
              href={detailApp.web_url}
              target="_blank"
              rel="noreferrer"
              class="flex items-center gap-1 text-sm text-fjord-accent hover:underline"
              >Website <Icon name="external" size={12} /></a
            >
          {/if}
          {#if detailApp.upstream_url}
            <a
              href={detailApp.upstream_url}
              target="_blank"
              rel="noreferrer"
              class="flex items-center gap-1 text-sm text-fjord-accent hover:underline"
              >Source <Icon name="external" size={12} /></a
            >
          {/if}
          <div class="flex-1"></div>
          <button
            on:click={() => { installingApp = detailApp; detailApp = null; }}
            class="bg-fjord-accent hover:bg-fjord-accent-hover text-white px-6 py-2 rounded-md font-medium shadow-lg">Install…</button>
        </div>
      </div>
    </div>
  {/if}

  {#if installingApp}
    <InstallWizard
      sources={installingApp.sources ?? [{ catalog: installingApp.catalog ?? '', catalog_name: installingApp.catalog_name ?? '', manifest_url: installingApp.manifest_url, version: installingApp.version, variants: installingApp.variants }]}
      appName={installingApp.name}
      appId={installingApp.id}
      appClass={installingApp.class}
      busy={installBusy}
      submitError={installError}
      on:close={() => { installingApp = null; installBusy = false; installError = ''; }}
      on:deploy={handleDeploy}
    />
  {/if}
</div>
