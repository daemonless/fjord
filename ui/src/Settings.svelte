<script lang="ts">
  import { onMount, createEventDispatcher } from 'svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import EmptyState from './EmptyState.svelte';
  import DirPicker from './DirPicker.svelte';
  import FolderRows from './FolderRows.svelte';
  import { toast } from './toast';

  type About = {
    version: string;
    engine: string;
    os: string;
    fjordRoot: string;
    stacksDir: string;
    catalogs: number;
    listen: string;
  };

  let about: About | null = null;
  let loading = true;
  let error = '';

  onMount(async () => {
    try {
      const res = await fetch('/api/about');
      if (!res.ok) throw new Error(await res.text());
      about = await res.json();
    } catch (e: any) {
      error = e.message || 'Failed to load daemon info';
    } finally {
      loading = false;
    }
  });


  // ---- engines (the first "plugin" category) ----
  type EngineRow = { name: string; description?: string; available: boolean; enabled: boolean; default: boolean; reason?: string; warning?: string; canInstall?: boolean };
  let engines: EngineRow[] = [];
  let defaultEngine = '';
  let installingEngine = '';
  let togglingEngine = '';
  async function toggleEngine(name: string, enabled: boolean) {
    togglingEngine = name;
    try {
      const res = await fetch('/api/engine/toggle', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, enabled }),
      });
      if (!res.ok) {
        // A blocked disable comes back as JSON with the offending stacks listed.
        // Read the body ONCE as text; a text/plain 409 would otherwise throw
        // "body stream already read" on the fallback and hide the real message.
        let msg = `HTTP ${res.status}`;
        const raw = (await res.text()).trim();
        try {
          const j = JSON.parse(raw);
          msg = j.error || msg;
          if (j.stacks?.length) msg += '\n• ' + j.stacks.join('\n• ');
        } catch {
          msg = raw || msg;
        }
        throw new Error(msg);
      }
      await loadEngines();
      toast(enabled ? `${name} enabled` : `${name} disabled`, { kind: 'success' });
    } catch (e: any) {
      toast(e.message || 'Could not change engine', { kind: 'error', timeout: 8000 });
    } finally {
      togglingEngine = '';
    }
  }
  $: availableEngines = engines.filter((e) => e.available);

  // Settings is organized into tabs; the active one lives in the URL
  // (#/settings/<tab>), owned by App: it arrives as a prop, changes go up.
  export let tab: 'storage' | 'extensions' | 'catalogs' | 'advanced' = 'storage';
  const dispatch = createEventDispatcher();
  let activeTab: 'storage' | 'extensions' | 'catalogs' | 'advanced' = tab;
  $: activeTab = tab;
  function selectTab(t: 'storage' | 'extensions' | 'catalogs' | 'advanced') {
    activeTab = t;
    dispatch('tab', t);
  }

  // ---- provider plugins (homelab, ...) ----
  type PluginRow = { name: string; label: string; description: string; enabled: boolean };
  let plugins: PluginRow[] = [];
  let togglingPlugin = '';
  async function loadPlugins() {
    try {
      const r = await fetch('/api/plugins');
      if (r.ok) plugins = (await r.json()).plugins || [];
    } catch {}
  }
  onMount(loadPlugins);
  async function togglePlugin(name: string, enabled: boolean) {
    togglingPlugin = name;
    try {
      const r = await fetch('/api/plugins', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, enabled }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      await Promise.all([loadPlugins(), loadFolderSets()]); // presets follow the plugin
      toast(enabled ? `${name} enabled` : `${name} disabled`, { kind: 'success' });
    } catch (e: any) {
      toast(e.message || 'Could not change plugin', { kind: 'error' });
    } finally {
      togglingPlugin = '';
    }
  }
  async function loadEngines() {
    try {
      const res = await fetch('/api/engine');
      if (res.ok) {
        const d = await res.json();
        engines = d.engines || [];
        defaultEngine = d.default || 'podman';
      }
    } catch {
      // no engine info -> section hidden
    }
  }
  onMount(loadEngines);

  async function setDefaultEngine(name: string) {
    if (name === defaultEngine) return;
    try {
      const res = await fetch('/api/engine', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ default: name }),
      });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      defaultEngine = (await res.json()).default;
      toast(`Default engine set to ${defaultEngine}`, { kind: 'success' });
      await loadEngines();
    } catch (e: any) {
      toast(e.message || 'Could not set default engine', { kind: 'error' });
    }
  }

  async function installEngine(name: string) {
    installingEngine = name;
    try {
      const res = await fetch('/api/engine/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name }),
      });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      const { restartRequired } = await res.json();
      if (restartRequired) toast(`${name} installed — restart fjordd to enable it`, { kind: 'success', timeout: 8000 });
      else toast(`${name} installed and ready`, { kind: 'success' });
      await loadEngines();
    } catch (e: any) {
      toast(e.message || `Could not install ${name}`, { kind: 'error' });
    } finally {
      installingEngine = '';
    }
  }

  // ---- App data locations (managed storage bases) ----
  // An ordered list; the first is the default. With more than one, the install
  // wizard offers a pick. No separate "default" concept in the UI: the daemon
  // falls back to <fjord files>/containers when the list is empty.
  let appData: string[] = [];
  let pickAppData: number | null = null; // DirPicker target row
  // Long-form detail lives in help panels so the page reads as one list + one list.
  const storageHelp =
    "The first location is the default. Add more (an SSD pool, a bulk disk) and the install wizard lets you pick one per app.\nChanges affect new installs only; installed apps keep their folders.\nIf a folder with the app's name already exists in a location it is kept as-is (data and ownership untouched), so to bring existing data in, install the app under that name.\nFolder sets can refer to the chosen location as {{appdata}} and to the app's folder name as {{stack}}.";
  const folderSetHelp =
    "Name a set of host folders once (your movies, your TV, your downloads). At install, any host-path field offers \"Add folder set…\" to drop them in.\nWith the Homelab extension on (Settings → Extensions), preset sets (Movies, TV, …) carry their icon and are picked up automatically by apps with a matching folder at install. Sets you name yourself show a folder icon and are added by hand.\nFolders may use {{stack}} (the app's folder name) and {{appdata}} (the App data folder), e.g. {{appdata}}/{{stack}}/data.";
  let savingStorage = false;
  function moveAppData(i: number, dir: -1 | 1) {
    const j = i + dir;
    if (j < 0 || j >= appData.length) return;
    const next = [...appData];
    [next[i], next[j]] = [next[j], next[i]];
    appData = next;
  }
  // Which inline help panel is open ('' = none); a click toggles, not a hover.
  let helpOpen: '' | 'appdata' | 'sets' = '';
  async function loadStorage() {
    try {
      const r = await fetch('/api/settings/storage');
      if (r.ok) {
        const d = await r.json();
        appData = d.locations || [d.base];
      }
    } catch {}
  }
  onMount(loadStorage);
  async function saveStorage() {
    savingStorage = true;
    try {
      const r = await fetch('/api/settings/storage', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ locations: appData.map((l) => l.trim()).filter(Boolean) }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      await loadStorage();
      toast('App data locations saved', { kind: 'success' });
    } catch (e: any) {
      toast(e.message || 'Save failed', { kind: 'error' });
    } finally {
      savingStorage = false;
    }
  }

  // ---- advanced: install wizard disclosure level ----
  let wizardDetail = 1;
  let savingWizard = false;
  async function loadWizard() {
    try {
      const r = await fetch('/api/settings/wizard');
      if (r.ok) wizardDetail = Number((await r.json()).detail) || 1;
    } catch {}
  }
  onMount(loadWizard);
  async function saveWizard(detail: number) {
    savingWizard = true;
    try {
      const r = await fetch('/api/settings/wizard', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ detail }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      wizardDetail = detail;
      toast(detail === 1 ? 'Install wizard back to essentials' : 'Install wizard detail saved', { kind: 'success' });
    } catch (e: any) {
      toast(e.message || 'Save failed', { kind: 'error' });
      await loadWizard();
    } finally {
      savingWizard = false;
    }
  }

  // ---- folder sets (homelab provider plugin) ----
  // A folder set is a named set of host folders; the install wizard offers
  // them as "Add folder set…" on any host-path field. No roles, no auto-matching.
  type FolderSet = { id: string; name: string; folders: string[]; match?: string };
  let folderSets: FolderSet[] = [];
  let setPresets: { name: string; match: string }[] = [];
  let savingSets = false;
  let setsDirty = false;
  async function loadFolderSets() {
    try {
      const r = await fetch('/api/folder-sets');
      if (r.ok) {
        const d = await r.json();
        folderSets = d.sets || [];
        setPresets = d.presets || [];
        setsDirty = false;
      }
    } catch {}
  }
  onMount(loadFolderSets);
  const touchSets = () => {
    folderSets = folderSets;
    setsDirty = true;
  };
  // Presets not yet defined, offered as one-click additions.
  $: unusedPresets = setPresets.filter((p) => !folderSets.some((l) => l.name.trim().toLowerCase() === p.name.toLowerCase()));
  // The name is a set's identity; flag repeats before the server refuses them.
  $: dupSetNames = new Set(
    folderSets.map((l) => l.name.trim().toLowerCase()).filter((n, i, all) => n && all.indexOf(n) !== i),
  );
  // Preset-backed sets get a kind icon (film, tv, ...) instead of a label;
  // custom sets a plain folder. Keyed by preset name, so renames keep it.
  const PRESET_ICON: Record<string, string> = { movies: 'film', tv: 'tv', music: 'music', downloads: 'download', books: 'book', audiobooks: 'headphones', ebooks: 'book', photos: 'image' };
  const setIcon = (l: FolderSet) => (l.match ? PRESET_ICON[l.name.trim().toLowerCase()] || 'folder' : 'folder');
  function addFolderSet(name = '', match = '') {
    folderSets = [...folderSets, { id: '', name, folders: [''], match }];
    setsDirty = true;
  }
  function removeFolderSet(i: number) {
    folderSets = folderSets.filter((_, x) => x !== i);
    setsDirty = true;
  }
  async function saveFolderSets() {
    savingSets = true;
    try {
      const r = await fetch('/api/folder-sets', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sets: folderSets }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      await loadFolderSets();
      toast('Folder sets saved', { kind: 'success' });
    } catch (e: any) {
      toast(e.message || 'Save failed', { kind: 'error' });
    } finally {
      savingSets = false;
    }
  }

  // ---- catalogs management ----
  type CatalogRow = { id: string; name: string; url: string; apps: number; enabled?: boolean; icon?: string; builtin?: boolean; fetchedAt?: string };
  // Relative time for "fetched 2h ago" style hints.
  const ago = (iso?: string) => {
    if (!iso) return '';
    const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
    if (s < 90) return 'just now';
    if (s < 5400) return `${Math.round(s / 60)}m ago`;
    if (s < 129600) return `${Math.round(s / 3600)}h ago`;
    return `${Math.round(s / 86400)}d ago`;
  };
  const until = (iso?: string) => {
    if (!iso) return '';
    const s = (new Date(iso).getTime() - Date.now()) / 1000;
    if (s <= 60) return 'now';
    if (s < 5400) return `in ${Math.round(s / 60)}m`;
    return `in ${Math.round(s / 3600)}h`;
  };

  // ---- automatic catalog refresh ----
  const REFRESH_LABELS: Record<string, string> = { off: 'Off', '1h': 'Hourly', '6h': 'Every 6 hours', '24h': 'Daily' };
  let refreshInterval = '6h';
  let refreshOptions: string[] = ['off', '1h', '6h', '24h'];
  let refreshDefault = '6h';
  let refreshLastRun = '';
  let refreshLastError = '';
  let refreshNextDue = '';
  let savingRefresh = false;
  async function loadRefresh() {
    try {
      const r = await fetch('/api/settings/catalog-refresh');
      if (r.ok) {
        const d = await r.json();
        refreshInterval = d.interval;
        refreshOptions = d.options || refreshOptions;
        refreshDefault = d.default || refreshDefault;
        refreshLastRun = d.lastRun || '';
        refreshLastError = d.lastError || '';
        refreshNextDue = d.nextDue || '';
      }
    } catch {}
  }
  onMount(loadRefresh);
  async function saveRefresh(interval: string) {
    savingRefresh = true;
    try {
      const r = await fetch('/api/settings/catalog-refresh', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ interval }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      await loadRefresh();
      toast(interval === 'off' ? 'Automatic refresh off' : `Catalogs refresh ${REFRESH_LABELS[interval].toLowerCase()}`, { kind: 'success' });
    } catch (e: any) {
      toast(e.message || 'Save failed', { kind: 'error' });
    } finally {
      savingRefresh = false;
    }
  }
  let catalogs: CatalogRow[] = [];
  let newName = '';
  let newURL = '';
  let adding = false;
  let busy = ''; // catalog id with an in-flight refresh
  let confirmDelete = '';
  $: movableCount = catalogs.filter((c) => !c.builtin).length;

  let defaultCatalogURL = '';
  async function loadCatalogs() {
    try {
      const res = await fetch('/api/catalogs');
      if (res.ok) catalogs = await res.json();
      const st = await fetch('/api/setup/state');
      if (st.ok) defaultCatalogURL = (await st.json()).defaultCatalog || '';
    } catch {
      // list stays stale; individual actions surface their own errors
    }
  }
  onMount(loadCatalogs);
  // The default catalog is an ordinary source; once removed, one click puts it back.
  $: hasDefault = !defaultCatalogURL || catalogs.some((c: any) => (c.url || '').replace(/\/+$/, '') === defaultCatalogURL.replace(/\/+$/, ''));
  function readdDefault() {
    newName = 'daemonless';
    newURL = defaultCatalogURL;
    addCatalog();
  }

  async function addCatalog() {
    if (!newName.trim() || !newURL.trim()) return;
    adding = true;
    try {
      const res = await fetch('/api/catalogs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: newName.trim(), url: newURL.trim() }),
      });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      const added = await res.json();
      toast(`Catalog "${added.name}" added — ${added.apps} apps`, { kind: 'success' });
      newName = '';
      newURL = '';
      await loadCatalogs();
    } catch (e: any) {
      toast(e.message || 'Add failed', { kind: 'error' });
      await loadCatalogs(); // an add can persist but fail its first fetch
    } finally {
      adding = false;
    }
  }

  async function refreshCatalog(id: string) {
    busy = id;
    try {
      const res = await fetch(`/api/catalogs/${encodeURIComponent(id)}/refresh`, { method: 'POST' });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      toast('Catalog refreshed', { kind: 'success' });
      await loadCatalogs();
    } catch (e: any) {
      toast(e.message || 'Refresh failed', { kind: 'error' });
    } finally {
      busy = '';
    }
  }

  // Enable/disable a catalog: kept configured + cached, but its apps are shown or
  // hidden in the store. No re-fetch needed, so it's instant.
  async function toggleCatalog(id: string, enabled: boolean) {
    try {
      const res = await fetch(`/api/catalogs/${encodeURIComponent(id)}/toggle`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled }),
      });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      await loadCatalogs();
      toast(enabled ? 'Catalog enabled' : 'Catalog disabled', { kind: 'success' });
    } catch (e: any) {
      toast(e.message || 'Could not change catalog', { kind: 'error' });
    }
  }

  // Reorder = set priority (top = highest). Drives icon coalesce + the install
  // wizard's repository order/default. Only configured catalogs move; the local
  // seed stays pinned at the bottom as the fallback.
  async function moveCatalog(idx: number, dir: -1 | 1) {
    const movable = catalogs.filter((c) => !c.builtin);
    const j = idx + dir;
    if (j < 0 || j >= movable.length) return;
    [movable[idx], movable[j]] = [movable[j], movable[idx]];
    try {
      const res = await fetch('/api/catalogs/reorder', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ order: movable.map((c) => c.id) }),
      });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      await loadCatalogs();
    } catch (e: any) {
      toast(e.message || 'Reorder failed', { kind: 'error' });
    }
  }

  // Drag-and-drop priority reordering (same effect as moveCatalog's arrows):
  // only configured catalogs participate; the builtin seed stays pinned last.
  let dragCat = '';
  let dropCat = ''; // row hovered during a drag
  let dropCatAfter = false; // drop below (vs above) the hovered row
  function onCatDragOver(e: DragEvent, id: string) {
    if (!dragCat || dragCat === id) return;
    e.preventDefault();
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    dropCatAfter = e.clientY > r.top + r.height / 2;
    dropCat = id;
  }
  function clearCatDrag() {
    dragCat = '';
    dropCat = '';
  }
  async function commitCatalogDrop(targetId: string) {
    const id = dragCat;
    if (!id || id === targetId) return clearCatDrag();
    const order = catalogs.filter((c) => !c.builtin).map((c) => c.id).filter((x) => x !== id);
    let idx = order.indexOf(targetId);
    idx = idx < 0 ? order.length : idx + (dropCatAfter ? 1 : 0);
    order.splice(idx, 0, id);
    clearCatDrag();
    try {
      const res = await fetch('/api/catalogs/reorder', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ order }),
      });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      await loadCatalogs();
    } catch (e: any) {
      toast(e.message || 'Reorder failed', { kind: 'error' });
    }
  }

  // Inline catalog rename: display name only; id/cache/priority unchanged.
  let editingCatalog: string | null = null;
  let catalogNameEdit = '';
  async function commitCatalogRename(id: string) {
    editingCatalog = null;
    const name = catalogNameEdit.trim();
    const cur = catalogs.find((c) => c.id === id);
    if (!name || !cur || name === cur.name) return;
    try {
      const res = await fetch(`/api/catalogs/${encodeURIComponent(id)}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name }),
      });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      toast(`Renamed to ${name}`, { kind: 'success' });
      await loadCatalogs();
    } catch (e: any) {
      toast(e.message || 'Rename failed', { kind: 'error' });
    }
  }

  async function removeCatalog(id: string) {
    confirmDelete = '';
    try {
      const res = await fetch(`/api/catalogs/${encodeURIComponent(id)}`, { method: 'DELETE' });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      toast('Catalog removed', { kind: 'success' });
      await loadCatalogs();
    } catch (e: any) {
      toast(e.message || 'Remove failed', { kind: 'error' });
    }
  }

  // Configuration is env-driven (rc.conf on the host), so this page shows the
  // effective values rather than editing them.
  // Engines get their own table below; the identity table is host/config only.
  $: rows = about
    ? [
        { label: 'Platform', value: about.os },
        { label: 'Listen Address', value: about.listen },
      ]
    : [];
</script>

<div class="flex flex-col h-full">
  <div class="mb-4 shrink-0">
    <h2 class="text-2xl font-bold text-fjord-fg">Settings</h2>
    <p class="text-sm text-fjord-fg-dim">Daemon identity and effective configuration.</p>
  </div>

  <div class="flex-1 overflow-y-auto">
    {#if loading}
      <div class="flex items-center gap-3 text-fjord-fg-dim text-sm"><Spinner size={18} /> Loading…</div>
    {:else if error}
      <EmptyState icon="alert" title="Daemon Info Unavailable" description={error} />
    {:else if about}
      <div class="flex items-center gap-3 mb-5">
        <div class="w-12 h-12 rounded-xl bg-fjord-accent/15 border border-fjord-accent/30 flex items-center justify-center text-fjord-accent">
          <Icon name="mountain" size={26} />
        </div>
        <div>
          <div class="text-lg font-bold text-fjord-fg">fjord</div>
          <div class="text-sm text-fjord-fg-dim font-mono">v{about.version}</div>
        </div>
      </div>

      <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border max-w-2xl">
        {#each rows as r}
          <div class="flex items-center gap-4 px-4 py-3">
            <span class="w-40 shrink-0 text-sm text-fjord-fg-muted">{r.label}</span>
            <span class="min-w-0 flex-1 text-sm text-fjord-fg-body font-mono truncate" title={r.value}>{r.value}</span>
          </div>
        {/each}
      </div>


      <!-- Storage / Extensions / Catalogs / Advanced tabs -->
      <div class="flex gap-1 mt-6 mb-4 border-b border-fjord-border max-w-2xl">
        <button
          on:click={() => selectTab('storage')}
          class="px-4 py-2 text-sm font-medium -mb-px border-b-2 transition-colors {activeTab === 'storage'
            ? 'border-fjord-accent text-fjord-fg'
            : 'border-transparent text-fjord-fg-muted hover:text-fjord-fg-body'}">Storage</button
        >
        <button
          on:click={() => selectTab('extensions')}
          class="px-4 py-2 text-sm font-medium -mb-px border-b-2 transition-colors {activeTab === 'extensions'
            ? 'border-fjord-accent text-fjord-fg'
            : 'border-transparent text-fjord-fg-muted hover:text-fjord-fg-body'}">Extensions</button
        >
        <button
          on:click={() => selectTab('catalogs')}
          class="px-4 py-2 text-sm font-medium -mb-px border-b-2 transition-colors {activeTab === 'catalogs'
            ? 'border-fjord-accent text-fjord-fg'
            : 'border-transparent text-fjord-fg-muted hover:text-fjord-fg-body'}">Catalogs</button
        >
        <button
          on:click={() => selectTab('advanced')}
          class="px-4 py-2 text-sm font-medium -mb-px border-b-2 transition-colors {activeTab === 'advanced'
            ? 'border-fjord-accent text-fjord-fg'
            : 'border-transparent text-fjord-fg-muted hover:text-fjord-fg-body'}">Advanced</button
        >
      </div>

      {#if activeTab === 'storage'}
      <!-- app data (managed storage base) -->
      <div class="max-w-2xl">
        <div class="flex items-center gap-1.5 mb-1.5">
          <h3 class="text-sm font-semibold text-fjord-fg-secondary">App data</h3>
          <button
            type="button"
            on:click={() => (helpOpen = helpOpen === 'appdata' ? '' : 'appdata')}
            aria-expanded={helpOpen === 'appdata'}
            aria-label="About the App data folder"
            class="flex items-center justify-center w-5 h-5 rounded-full text-fjord-fg-dim hover:text-fjord-fg hover:bg-fjord-border transition-colors {helpOpen === 'appdata' ? 'text-fjord-fg bg-fjord-border' : ''}"
            ><Icon name="help" size={13} /></button
          >
        </div>
        {#if helpOpen === 'appdata'}
          <div class="text-xs text-fjord-fg-muted bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 mb-2 whitespace-pre-line leading-relaxed">{storageHelp}</div>
        {/if}
        <div class="space-y-1.5">
          {#each appData as _, i (i)}
            <div class="flex items-center gap-2">
              <input
                bind:value={appData[i]}
                spellcheck="false"
                placeholder="/containers"
                class="flex-1 min-w-0 bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-sm text-fjord-fg-body font-mono focus:outline-none focus:border-fjord-accent"
              />
              {#if i === 0}
                <span class="shrink-0 text-[10px] font-semibold uppercase tracking-wide text-fjord-fg-dim border border-fjord-border rounded px-1.5 py-0.5">default</span>
              {/if}
              <button on:click={() => (pickAppData = i)} title="Browse" class="shrink-0 px-2 py-1.5 rounded-md text-xs bg-fjord-inset border border-fjord-border text-fjord-fg-secondary hover:text-fjord-fg">Browse…</button>
              {#if appData.length > 1}
                <button on:click={() => moveAppData(i, -1)} disabled={i === 0} title="Move up (the top one is the default)" class="shrink-0 text-fjord-fg-dim hover:text-fjord-fg disabled:opacity-30">↑</button>
                <button on:click={() => moveAppData(i, 1)} disabled={i === appData.length - 1} title="Move down" class="shrink-0 text-fjord-fg-dim hover:text-fjord-fg disabled:opacity-30">↓</button>
                <button on:click={() => (appData = appData.filter((_, x) => x !== i))} title="Remove location" class="shrink-0 text-fjord-fg-dim hover:text-fjord-danger transition-colors"><Icon name="trash" size={15} /></button>
              {/if}
            </div>
          {/each}
        </div>
        <div class="flex items-center gap-2 mt-2">
          <button on:click={() => (appData = [...appData, ''])} class="text-xs text-fjord-fg-muted hover:text-fjord-fg flex items-center gap-1"><Icon name="plus" size={12} /> Add location</button>
          <button
            on:click={saveStorage}
            disabled={savingStorage}
            class="ml-auto shrink-0 bg-fjord-accent hover:bg-fjord-accent-hover text-white text-sm font-medium py-1.5 px-4 rounded-lg disabled:opacity-50"
            >{savingStorage ? 'Saving…' : 'Save'}</button
          >
        </div>
        <p class="text-xs text-fjord-fg-dim mt-1.5">
          Each app gets its own folder in the location chosen at install, like
          <span class="font-mono text-fjord-fg-muted">{(appData[0] || '').trim().replace(/\/+$/, '') || '…'}/radarr</span>
          — created at install; an existing folder is kept as-is.
        </p>
        <p class="text-xs text-fjord-fg-faint mt-3">
          fjord keeps its own files (compose files, settings, catalog cache) in
          <span class="font-mono">{about.fjordRoot}</span>. Nothing of yours lives there.
        </p>
      </div>

        <!-- Folder sets (homelab provider plugin) -->
        <div class="max-w-2xl mt-8">
          <div class="flex items-center gap-1.5 mb-1">
            <h3 class="text-sm font-semibold text-fjord-fg-secondary">Folder sets</h3>
            <button
              type="button"
              on:click={() => (helpOpen = helpOpen === 'sets' ? '' : 'sets')}
              aria-expanded={helpOpen === 'sets'}
              aria-label="About folder sets"
              class="flex items-center justify-center w-5 h-5 rounded-full text-fjord-fg-dim hover:text-fjord-fg hover:bg-fjord-border transition-colors {helpOpen === 'sets' ? 'text-fjord-fg bg-fjord-border' : ''}"
              ><Icon name="help" size={13} /></button
            >
          </div>
          {#if helpOpen === 'sets'}
            <div class="text-xs text-fjord-fg-muted bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 mb-2 whitespace-pre-line leading-relaxed">{folderSetHelp}</div>
          {/if}
          <p class="text-xs text-fjord-fg-dim mb-3">Reusable host folders, offered on any path field at install.</p>

          {#each folderSets as lib, i (i)}
            <div class="border border-fjord-border rounded-xl p-3 mb-3">
              <div class="flex items-center gap-2 mb-2">
                <span
                  class="shrink-0 w-7 h-7 rounded-md border flex items-center justify-center {lib.match ? 'text-fjord-accent border-fjord-accent/40 bg-fjord-accent/10' : 'text-fjord-fg-dim border-fjord-border bg-fjord-inset'}"
                  title={lib.match ? `Apps with a ${lib.name.trim().toLowerCase() || 'matching'} folder get this set at install` : 'Added to apps by hand'}
                  ><Icon name={setIcon(lib)} size={15} /></span
                >
                <input
                  list="set-presets"
                  bind:value={lib.name}
                  on:input={touchSets}
                  placeholder="Name (Movies)"
                  class="flex-1 min-w-0 bg-fjord-inset border rounded-md px-2 py-1.5 text-sm text-fjord-fg-body focus:outline-none focus:border-fjord-accent {dupSetNames.has(lib.name.trim().toLowerCase()) ? 'border-fjord-danger/60' : 'border-fjord-border'}"
                />
                <button on:click={() => removeFolderSet(i)} title="Remove folder set" class="shrink-0 text-fjord-fg-dim hover:text-fjord-danger transition-colors"><Icon name="trash" size={15} /></button>
              </div>
              {#if dupSetNames.has(lib.name.trim().toLowerCase())}
                <p class="text-xs text-fjord-danger mb-2">Another folder set already has this name — rename it or move its folders into the other one.</p>
              {/if}

              <FolderRows bind:folders={lib.folders} name={lib.name} on:change={touchSets} />
            </div>
          {/each}
          <datalist id="set-presets">{#each setPresets as p}<option value={p.name}></option>{/each}</datalist>

          <div class="flex items-center gap-2 flex-wrap">
            <button on:click={() => addFolderSet()} class="flex items-center gap-1.5 text-sm text-fjord-fg-secondary hover:text-fjord-fg border border-fjord-border rounded-lg px-3 py-1.5"><Icon name="plus" size={13} /> Add folder set</button>
            {#each unusedPresets as p}
              <button on:click={() => addFolderSet(p.name, p.match)} title="Add a {p.name} set that apps pick up automatically" class="flex items-center gap-1 text-xs text-fjord-fg-muted hover:text-fjord-fg border border-fjord-border/60 rounded-lg px-2 py-1"><Icon name={PRESET_ICON[p.name.toLowerCase()] || 'folder'} size={12} /> {p.name}</button>
            {/each}
            {#if setsDirty}
              <button on:click={saveFolderSets} disabled={savingSets || dupSetNames.size > 0} title={dupSetNames.size ? 'Fix duplicate folder set names first' : ''} class="ml-auto bg-fjord-accent hover:bg-fjord-accent-hover text-white text-sm font-medium py-1.5 px-4 rounded-lg disabled:opacity-50">{savingSets ? 'Saving…' : 'Save folder sets'}</button>
            {/if}
          </div>
        </div>

      {/if}

      {#if activeTab === 'extensions'}
        <div class="max-w-2xl">
          <h3 class="text-sm font-semibold text-fjord-fg-secondary mb-1">Engines</h3>
          <p class="text-xs text-fjord-fg-dim mb-3">
            Container runtimes stacks can run on. <b>Enable</b> the ones this host should use; the
            <b>default</b> (checkmark) is used by new installs unless you pick another in the wizard. A stack's
            engine is fixed once installed, so an engine can't be disabled while stacks run on it.
          </p>
          <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border">
            {#each engines as e (e.name)}
              <div class="flex items-center gap-3 px-4 py-3">
                <button
                  on:click={() => e.enabled && setDefaultEngine(e.name)}
                  disabled={!e.enabled}
                  title={e.enabled ? (e.default ? 'Default engine' : 'Set as default') : 'Enable this engine first'}
                  class="shrink-0 w-6 h-6 rounded-full border flex items-center justify-center transition-colors {e.default
                    ? 'bg-fjord-accent border-fjord-accent text-white'
                    : e.enabled
                      ? 'border-fjord-border text-transparent hover:border-fjord-accent'
                      : 'border-fjord-border/50 text-transparent cursor-not-allowed opacity-40'}"
                  ><Icon name="check" size={13} /></button
                >
                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-2">
                    <span class="text-sm font-medium text-fjord-fg">{e.name}</span>
                    {#if e.default}<span class="text-[10px] font-semibold uppercase tracking-wide text-fjord-accent">default</span>{/if}
                    {#if e.available && !e.enabled}<span class="text-[10px] font-medium px-1.5 py-0.5 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-dim">disabled</span>{/if}
                    {#if !e.available}<span class="text-[10px] font-medium px-1.5 py-0.5 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-dim">not installed</span>{/if}
                  </div>
                  <div class="text-xs text-fjord-fg-dim truncate" title={e.available ? e.description : e.reason}>
                    {e.available ? e.description : e.reason || e.description}
                  </div>
                  {#if e.warning}
                    <div class="flex items-start gap-1.5 text-xs text-fjord-warning mt-1">
                      <Icon name="alert" size={12} class="shrink-0 mt-0.5" /> <span>{e.warning}</span>
                    </div>
                  {/if}
                </div>
                {#if !e.available && e.canInstall}
                  <button
                    on:click={() => installEngine(e.name)}
                    disabled={installingEngine === e.name}
                    class="shrink-0 flex items-center gap-1.5 bg-fjord-accent hover:bg-fjord-accent-hover text-white text-sm font-medium py-1.5 px-3 rounded-lg disabled:opacity-50"
                    >{#if installingEngine === e.name}<Spinner size={13} /> Installing…{:else}<Icon name="plus" size={13} /> Install{/if}</button
                  >
                {:else if e.available}
                  <!-- enable/disable toggle switch -->
                  <button
                    on:click={() => toggleEngine(e.name, !e.enabled)}
                    disabled={togglingEngine === e.name}
                    title={e.enabled ? 'Disable this engine' : 'Enable this engine'}
                    aria-label={e.enabled ? 'Disable ' + e.name : 'Enable ' + e.name}
                    class="shrink-0 relative w-11 h-6 rounded-full transition-colors disabled:opacity-50 {e.enabled ? 'bg-fjord-accent' : 'bg-fjord-border'}"
                  >
                    <span class="absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform {e.enabled ? 'translate-x-5' : ''}"></span>
                  </button>
                {/if}
              </div>
            {/each}
          </div>
        </div>

        <!-- features (provider plugins) -->
        <div class="max-w-2xl mt-8">
          <h3 class="text-sm font-semibold text-fjord-fg-secondary mb-1">Features</h3>
          <p class="text-xs text-fjord-fg-dim mb-3">Optional behaviours on top of the core. Homelab adds the Movies, TV, Music… presets under Storage → Folder sets and their automatic pick-up at install.</p>
          <div class="border border-fjord-border rounded-xl divide-y divide-fjord-border">
              {#each plugins as p (p.name)}
                <div class="flex items-center gap-3 px-4 py-3">
                  <div class="min-w-0 flex-1">
                    <div class="flex items-center gap-2">
                      <span class="text-sm font-medium text-fjord-fg">{p.label}</span>
                      {#if !p.enabled}<span class="text-[10px] font-medium px-1.5 py-0.5 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-dim">off</span>{/if}
                    </div>
                    <div class="text-xs text-fjord-fg-dim">{p.description}</div>
                  </div>
                  <button
                    on:click={() => togglePlugin(p.name, !p.enabled)}
                    disabled={togglingPlugin === p.name}
                    title={p.enabled ? `Turn ${p.label} off` : `Turn ${p.label} on`}
                    aria-label={p.enabled ? 'Disable ' + p.label : 'Enable ' + p.label}
                    class="shrink-0 relative w-11 h-6 rounded-full transition-colors disabled:opacity-50 {p.enabled ? 'bg-fjord-accent' : 'bg-fjord-border'}"
                  >
                    <span class="absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform {p.enabled ? 'translate-x-5' : ''}"></span>
                  </button>
                </div>
              {/each}
          </div>
        </div>



      {/if}

      {#if activeTab === 'catalogs'}
      <div class="max-w-2xl">
        <h3 class="text-sm font-semibold text-fjord-fg-secondary mb-1">Catalogs</h3>
        <p class="text-xs text-fjord-fg-dim mb-3">
          App sources for the store. Each is a base URL publishing <code>catalog.json</code>, icons and
          manifests — another fjord instance's <code>/catalog</code> URL also works. Apps from every
          catalog appear together; order sets <b>priority</b> (top wins) — the highest-priority catalog
          provides an app's icon and is the default repository when installing.
        </p>

        <!-- automatic refresh -->
        <div class="flex flex-wrap items-center gap-x-3 gap-y-2 mb-4">
          <span class="text-xs font-semibold text-fjord-fg-muted">Refresh automatically</span>
          <div class="flex items-center gap-1">
            {#each refreshOptions as o}
              <button
                on:click={() => refreshInterval !== o && saveRefresh(o)}
                disabled={savingRefresh}
                class="px-2.5 py-1 rounded-md text-xs font-medium transition-colors {refreshInterval === o
                  ? 'bg-fjord-accent text-white'
                  : 'bg-fjord-card border border-fjord-border text-fjord-fg-secondary hover:text-fjord-fg'}"
                >{REFRESH_LABELS[o] ?? o}{#if o === refreshDefault && refreshInterval !== o}<span class="ml-1 text-[9px] uppercase tracking-wide opacity-60">default</span>{/if}</button
              >
            {/each}
          </div>
          <span class="text-[11px] text-fjord-fg-dim">
            {#if refreshInterval === 'off'}Only the Refresh buttons update the store.
            {:else if refreshLastError}<span class="text-fjord-warning">Last automatic refresh failed: {refreshLastError}</span>
            {:else if refreshNextDue}Next {until(refreshNextDue)}{#if refreshLastRun} · last run {ago(refreshLastRun)}{/if}
            {/if}
          </span>
        </div>

        {#if catalogs.length > 0}
          <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border mb-3">
            {#each catalogs as c, i (c.id)}
              <!-- svelte-ignore a11y-no-static-element-interactions -->
              <div
                draggable={!c.builtin && movableCount > 1}
                on:dragstart={() => (dragCat = c.id)}
                on:dragend={clearCatDrag}
                on:dragover={(e) => onCatDragOver(e, c.id)}
                on:drop={() => commitCatalogDrop(c.id)}
                class="flex items-center gap-3 px-4 py-3 border-y-2 border-transparent transition-colors {!c.builtin &&
                movableCount > 1
                  ? 'cursor-grab active:cursor-grabbing'
                  : ''} {dragCat === c.id ? 'opacity-40' : ''} {dropCat === c.id && !dropCatAfter
                  ? '!border-t-fjord-accent'
                  : ''} {dropCat === c.id && dropCatAfter ? '!border-b-fjord-accent' : ''}"
              >
                {#if !c.builtin && movableCount > 1}
                  <div class="shrink-0 flex flex-col -my-1">
                    <button
                      on:click={() => moveCatalog(i, -1)}
                      disabled={i === 0}
                      title="Higher priority"
                      class="text-fjord-fg-dim hover:text-fjord-fg disabled:opacity-30 disabled:hover:text-fjord-fg-dim"
                      ><Icon name="chevron-up" size={14} /></button
                    >
                    <button
                      on:click={() => moveCatalog(i, 1)}
                      disabled={i === movableCount - 1}
                      title="Lower priority"
                      class="text-fjord-fg-dim hover:text-fjord-fg disabled:opacity-30 disabled:hover:text-fjord-fg-dim"
                      ><Icon name="chevron-down" size={14} /></button
                    >
                  </div>
                {/if}
                <div class="shrink-0 w-8 h-8 rounded-md bg-fjord-bg border border-fjord-border flex items-center justify-center overflow-hidden text-fjord-fg-muted">
                  {#if c.icon}
                    <img src={c.icon} alt={c.name} class="w-5 h-5 object-contain" />
                  {:else}
                    <Icon name={c.builtin ? 'drive' : 'globe'} size={16} />
                  {/if}
                </div>
                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-2">
                    {#if editingCatalog === c.id}
                      <!-- svelte-ignore a11y-autofocus -->
                      <input
                        autofocus
                        bind:value={catalogNameEdit}
                        on:keydown={(e) => { if (e.key === 'Enter') commitCatalogRename(c.id); else if (e.key === 'Escape') editingCatalog = null; }}
                        on:blur={() => (editingCatalog = null)}
                        class="text-sm font-medium text-fjord-fg bg-fjord-inset border border-fjord-accent rounded px-1.5 py-0.5 min-w-0 focus:outline-none"
                      />
                    {:else if c.builtin}
                      <span class="text-sm font-medium text-fjord-fg">{c.name}</span>
                    {:else}
                      <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
                      <span
                        on:click={() => { editingCatalog = c.id; catalogNameEdit = c.name; }}
                        title="Click to rename"
                        class="text-sm font-medium text-fjord-fg cursor-text hover:bg-fjord-border/40 rounded px-1 -mx-1"
                      >{c.name}</span>
                    {/if}
                    <span class="text-[11px] text-fjord-fg-dim">{c.apps} apps{#if c.fetchedAt} · fetched {ago(c.fetchedAt)}{/if}</span>
                    {#if c.enabled === false}<span class="text-[10px] font-medium px-1.5 py-0.5 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-dim">disabled</span>{/if}
                  </div>
                  <div class="text-xs text-fjord-fg-dim font-mono truncate" title={c.url}>
                    {c.url || 'seeded on disk — remove to stop serving it'}
                  </div>
                </div>
                {#if !c.builtin}
                  <button
                    on:click={() => toggleCatalog(c.id, !(c.enabled ?? true))}
                    title={(c.enabled ?? true) ? 'Disable — hide its apps from the store' : 'Enable — show its apps in the store'}
                    aria-label={(c.enabled ?? true) ? 'Disable ' + c.name : 'Enable ' + c.name}
                    class="shrink-0 relative w-11 h-6 rounded-full transition-colors {(c.enabled ?? true) ? 'bg-fjord-accent' : 'bg-fjord-border'}"
                  >
                    <span class="absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform {(c.enabled ?? true) ? 'translate-x-5' : ''}"></span>
                  </button>
                  <button
                    on:click={() => refreshCatalog(c.id)}
                    disabled={busy === c.id}
                    title="Re-fetch this catalog"
                    class="shrink-0 flex items-center justify-center w-7 h-7 rounded-md text-fjord-fg-muted hover:text-fjord-fg hover:bg-fjord-border transition-colors disabled:opacity-50"
                    ><Icon name="refresh" size={14} class={busy === c.id ? 'animate-spin' : ''} /></button
                  >
                {/if}
                {#if confirmDelete === c.id}
                  <button on:click={() => removeCatalog(c.id)} class="text-xs px-2 py-1 rounded bg-fjord-danger hover:bg-fjord-danger-hover text-white">Remove</button>
                  <button on:click={() => (confirmDelete = '')} class="text-xs px-2 py-1 rounded text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
                {:else}
                  <button
                    on:click={() => (confirmDelete = c.id)}
                    class="text-xs px-2 py-1 rounded text-fjord-fg-muted hover:text-fjord-danger hover:bg-fjord-border transition-colors"
                    >Remove</button
                  >
                {/if}
              </div>
            {/each}
          </div>
        {:else}
          <p class="text-xs text-fjord-fg-faint italic mb-3">No catalogs configured — the App Store is empty.</p>
        {/if}

        <div class="flex gap-2">
          <input
            bind:value={newName}
            placeholder="name (e.g. daemonless)"
            class="w-44 shrink-0 bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-sm text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
          />
          <input
            bind:value={newURL}
            placeholder={defaultCatalogURL || 'https://…/v1/<source>'}
            class="flex-1 min-w-0 bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-sm text-fjord-fg-body font-mono focus:outline-none focus:border-fjord-accent"
          />
          <button
            on:click={addCatalog}
            disabled={adding || !newName.trim() || !newURL.trim()}
            class="shrink-0 flex items-center gap-2 bg-fjord-accent hover:bg-fjord-accent-hover text-white text-sm font-medium py-2 px-4 rounded-lg disabled:opacity-50"
            ><Icon name="plus" size={13} /> {adding ? 'Adding…' : 'Add'}</button
          >
        </div>
        {#if !hasDefault}
          <button
            on:click={readdDefault}
            disabled={adding}
            class="mt-3 flex items-center gap-1.5 text-xs font-medium text-fjord-fg-muted hover:text-fjord-fg py-1 px-2.5 rounded-md border border-fjord-border hover:border-fjord-accent/40 transition-colors disabled:opacity-40"
            ><Icon name="plus" size={12} /> Re-add the daemonless catalog</button
          >
        {/if}
      </div>

      {/if}

      <p class="text-xs text-fjord-fg-faint mt-6 max-w-2xl">
        Everything else comes from the daemon's environment (<code>FJORD_*</code> variables — via rc.conf on FreeBSD
        or the container environment). Host readiness checks live on the <b>System</b> page.
      </p>
    {/if}

    {#if activeTab === 'advanced'}
      <div class="max-w-2xl">
        <h3 class="text-sm font-semibold text-fjord-fg-secondary mb-1">Install wizard detail</h3>
        <p class="text-xs text-fjord-fg-dim mb-3">
          How much the install wizard shows before you click anything. The defaults are chosen so an app works
          without touching them; every field is still one click away at the lowest level. Raise this only if
          you change the same fields on every install and know what they do.
        </p>
        <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border">
          {#each [
            { v: 1, label: 'Essentials only', hint: 'Required fields and the stack name. Options and Advanced stay folded.' },
            { v: 2, label: 'Expand Options', hint: 'Also opens paths, ports and image tags on every install.' },
            { v: 3, label: 'Expand everything', hint: 'Also opens Advanced: engine, networks, raw environment. For people who read the compose file anyway.' },
          ] as o (o.v)}
            <div class="flex items-center gap-3 px-4 py-3">
              <button
                on:click={() => wizardDetail !== o.v && saveWizard(o.v)}
                disabled={savingWizard}
                title={wizardDetail === o.v ? 'Current level' : 'Use this level'}
                aria-label="Install wizard detail: {o.label}"
                class="shrink-0 w-6 h-6 rounded-full border flex items-center justify-center transition-colors {wizardDetail === o.v
                  ? 'bg-fjord-accent border-fjord-accent text-white'
                  : 'border-fjord-border text-transparent hover:border-fjord-accent'}"
                ><Icon name="check" size={13} /></button
              >
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <span class="text-sm font-medium text-fjord-fg">{o.label}</span>
                  {#if o.v === 1}<span class="text-[10px] font-semibold uppercase tracking-wide text-fjord-success">recommended</span>{/if}
                </div>
                <div class="text-xs text-fjord-fg-dim">{o.hint}</div>
              </div>
            </div>
          {/each}
        </div>

        <h3 class="text-sm font-semibold text-fjord-fg-secondary mt-8 mb-1">First-run setup</h3>
        <p class="text-xs text-fjord-fg-dim mb-3">Storage, Homelab folder presets, catalog and the quick guide, as shown on first launch. Nothing is reset; it only walks through the same settings again.</p>
        <button
          on:click={async () => {
            try {
              await fetch('/api/setup/state', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ done: false }) });
            } catch {}
            location.hash = '#/setup';
          }}
          class="px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-border hover:bg-fjord-accent hover:text-white transition-colors">Run setup again</button
        >
      </div>
    {/if}
  </div>
</div>

{#if pickAppData !== null}
  <DirPicker
    start={appData[pickAppData] || '/'}
    on:select={(e) => {
      if (pickAppData !== null) appData[pickAppData] = e.detail;
      appData = appData;
      pickAppData = null;
    }}
    on:close={() => (pickAppData = null)}
  />
{/if}
