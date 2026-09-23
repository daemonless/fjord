<script lang="ts">
  // First-run setup: four short screens, all skippable, every write goes
  // through the same APIs Settings uses. Finishing (or skipping) marks setup
  // done; Settings → Advanced can run it again.
  import { onMount, createEventDispatcher } from 'svelte';
  import Icon from './Icon.svelte';
  import EngineMark from './EngineMark.svelte';
  import { listCandidates, adoptContainer, startStack, type Candidate } from './adopt';
  import Spinner from './Spinner.svelte';
  import DirPicker from './DirPicker.svelte';
  import FolderRows from './FolderRows.svelte';
  import FixSnippet from './FixSnippet.svelte';
  import { toast } from './toast';

  const dispatch = createEventDispatcher<{ done: void }>();
  const STEPS = ['Welcome', 'Engine', 'Storage', 'Catalog', 'Already running', 'How it works'];
  // "Already running" only exists when something already is. Listing a step and
  // then jumping over it reads as a bug, so the indicator omits it entirely
  // when there is nothing to adopt. Entries carry their real index: `step`
  // still counts through STEPS, and hiding a label must not shift it.
  const ADOPT_STEP = 4;
  $: visibleSteps = STEPS.map((label, i) => ({ label, i })).filter(
    (s) => s.i !== ADOPT_STEP || candidates.length > 0,
  );
  let step = 0;

  // ---- 1. readiness ----
  type Check = { id: string; name: string; status: 'ok' | 'warn' | 'fail' | 'unknown'; detail?: string; why?: string; fix?: string };
  let checks: Check[] = [];
  let engineName = '';
  let hostMode = false;
  let checksLoading = true;
  async function loadChecks() {
    checksLoading = true;
    try {
      const [setupRes, engineRes] = await Promise.all([fetch('/api/setup'), fetch('/api/engine')]);
      if (setupRes.ok) {
        const rep = await setupRes.json();
        checks = rep.checks || [];
        hostMode = rep.mode === 'host';
      }
      if (engineRes.ok) {
        const d = await engineRes.json();
        engineName = d.default || '';
        engines = d.engines || [];
        engineChoice = engineName;
      }
    } catch {}
    checksLoading = false;
  }

  // ---- 1b. default engine ----
  // Which runtime new installs use unless the install wizard picks another.
  // Every engine is listed, unavailable ones greyed with the reason, so a
  // host with only one still learns what the other would need.
  type Engine = { name: string; description?: string; available: boolean; enabled: boolean; default: boolean; reason?: string; warning?: string };
  let engines: Engine[] = [];
  let engineChoice = '';
  let savingEngine = false;
  async function saveEngine(): Promise<boolean> {
    if (!engineChoice || engineChoice === engineName) return true;
    savingEngine = true;
    try {
      const r = await fetch('/api/engine', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ default: engineChoice }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      engineName = engineChoice;
      return true;
    } catch (e: any) {
      toast(`Could not set the default engine: ${e.message}`, { kind: 'error' });
      return false;
    } finally {
      savingEngine = false;
    }
  }
  $: failing = checks.filter((c) => c.status === 'fail');

  // ---- 2. storage + homelab ----
  type FolderSet = { id?: string; name: string; folders: string[]; match?: string };
  let appData = '';
  let appDataDefault = '';
  let pickDir = false;
  let homelab = true;
  let savingStorage = false;
  // Homelab presets (Movies, TV, ...) and their host paths. With homelab on
  // the wizard asks where each preset folder lives so apps pick them up at
  // install; off, it asks nothing. Presets and their match come from the API
  // (never hardcoded here). customSets are user-defined sets we must preserve
  // across the whole-list PUT.
  let presets: { name: string; match: string }[] = [];
  // Folders per preset (host paths and remote nfs://smb:// rows), edited with
  // the same FolderRows editor Settings uses. Seeded from any existing set so
  // multi-folder / remote presets round-trip whole. Ids preserved for update.
  let presetFolders: Record<string, string[]> = {};
  let presetIds: Record<string, string> = {};
  let customSets: FolderSet[] = [];
  // Icon per preset name, matching Settings' folder-set list.
  const PRESET_ICON: Record<string, string> = {
    movies: 'film', tv: 'tv', music: 'music', downloads: 'download', books: 'book', photos: 'image',
  };
  const presetIcon = (name: string) => PRESET_ICON[name.trim().toLowerCase()] || 'folder';

  async function loadStorage() {
    try {
      const r = await fetch('/api/settings/storage');
      if (r.ok) {
        const d = await r.json();
        appData = (d.locations && d.locations[0]) || d.base || '';
        appDataDefault = d.default || '';
      }
      const p = await fetch('/api/plugins');
      if (p.ok) {
        const row = ((await p.json()).plugins || []).find((x: any) => x.name === 'homelab');
        if (row) homelab = !!row.enabled;
      }
      await loadFolderSets();
    } catch {}
  }
  // Split saved sets into presets (match-bearing, or a known preset name) and
  // custom sets. Preset paths seed the reveal; custom sets ride through save
  // untouched so re-running setup never drops them.
  async function loadFolderSets() {
    try {
      const r = await fetch('/api/folder-sets');
      if (!r.ok) return;
      const d = await r.json();
      presets = d.presets || [];
      const presetNames = new Set(presets.map((p) => p.name.trim().toLowerCase()));
      const folders: Record<string, string[]> = {};
      const ids: Record<string, string> = {};
      const custom: FolderSet[] = [];
      for (const s of (d.sets || []) as FolderSet[]) {
        const isPreset = !!s.match || presetNames.has((s.name || '').trim().toLowerCase());
        if (isPreset) {
          folders[s.name] = [...(s.folders || [])];
          if (s.id) ids[s.name] = s.id;
        } else custom.push(s);
      }
      // Every preset gets an array to bind to; unconfigured ones start empty.
      for (const pr of presets) if (!folders[pr.name]) folders[pr.name] = [];
      presetFolders = folders;
      presetIds = ids;
      customSets = custom;
    } catch {}
  }
  // The presets list is gated on the plugin server-side, so persist the toggle
  // immediately and re-fetch: flipping on from off then reveals real presets
  // (with their match), flipping off hides them.
  async function toggleHomelab() {
    homelab = !homelab;
    try {
      await fetch('/api/plugins', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: 'homelab', enabled: homelab }),
      });
      await loadFolderSets();
    } catch {}
  }
  async function saveStorage(): Promise<boolean> {
    savingStorage = true;
    try {
      const loc = appData.trim();
      const r = await fetch('/api/settings/storage', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ locations: loc ? [loc] : [] }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      const p = await fetch('/api/plugins', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: 'homelab', enabled: homelab }),
      });
      if (!p.ok) throw new Error((await p.text()).trim() || `HTTP ${p.status}`);
      // Only touch folder sets when homelab is on; off, leave them as-is.
      // Merge preset rows the user actually filled in with the preserved
      // custom sets, then replace the whole list.
      if (homelab) {
        const rows: FolderSet[] = [];
        for (const pr of presets) {
          const folders = (presetFolders[pr.name] || []).map((f) => f.trim()).filter(Boolean);
          if (!folders.length) continue; // empty preset => not saved
          rows.push({ id: presetIds[pr.name], name: pr.name, match: pr.match, folders });
        }
        const fs = await fetch('/api/folder-sets', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ sets: [...customSets, ...rows] }),
        });
        if (!fs.ok) throw new Error((await fs.text()).trim() || `HTTP ${fs.status}`);
      }
      return true;
    } catch (e: any) {
      toast(e.message || 'Could not save storage settings', { kind: 'error' });
      return false;
    } finally {
      savingStorage = false;
    }
  }

  // ---- 3. catalog ----
  type CatalogRow = { id: string; name: string; url: string; apps: number; icon?: string; builtin?: boolean };
  let catalogs: CatalogRow[] = [];
  let catalogURL = ''; // "add another" input; the default catalog is already configured
  let defaultCatalogURL = '';
  let addingCatalog = false;
  async function loadCatalog() {
    try {
      const st = await fetch('/api/setup/state');
      if (st.ok) defaultCatalogURL = (await st.json()).defaultCatalog || '';
      const r = await fetch('/api/catalogs');
      if (r.ok) catalogs = ((await r.json()) || []).filter((c: CatalogRow) => !c.builtin);
    } catch {}
  }
  // Removing is a DELETE on the spot, so it takes the same two clicks as
  // removing one under Settings → Catalogs.
  let confirmRemove = '';
  async function removeCatalog(c: CatalogRow) {
    try {
      const r = await fetch(`/api/catalogs/${encodeURIComponent(c.id)}`, { method: 'DELETE' });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      toast(`Removed ${c.name}`, { kind: 'info' });
      await loadCatalog();
    } catch (e: any) {
      toast(e.message || 'Could not remove the catalog', { kind: 'error' });
    }
  }
  // The default catalog is just another source: once removed it can be put
  // back with one click instead of retyping the URL.
  $: hasDefault = !defaultCatalogURL || catalogs.some((c) => c.url.replace(/\/+$/, '') === defaultCatalogURL.replace(/\/+$/, ''));
  async function addCatalog(raw = catalogURL) {
    addingCatalog = true;
    try {
      const url = raw.trim().replace(/\/catalog\.json$/, '').replace(/\/+$/, '');
      let name = 'daemonless';
      try {
        const u = new URL(url);
        name = u.pathname.split('/').filter(Boolean).pop() || u.hostname.split('.')[0] || name;
      } catch {}
      const r = await fetch('/api/catalogs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, url }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
      const added = await r.json();
      toast(`Catalog added — ${added.apps} apps`, { kind: 'success' });
      catalogURL = '';
      await loadCatalog();
    } catch (e: any) {
      toast(e.message || 'Could not add the catalog', { kind: 'error' });
    } finally {
      addingCatalog = false;
    }
  }
  $: totalApps = catalogs.reduce((n, c) => n + (c.apps || 0), 0);

  // ---- 4. containers already on the host ----
  // Anything running that no stack owns can become a stack right here: same
  // image, mounts, network address and name. The step is skipped when there
  // is nothing to adopt.
  let candidates: Candidate[] = [];
  let picked: Record<string, boolean> = {};
  let adopting = false;
  let adoptProgress = '';
  let adoptedCount = 0;
  async function loadCandidates() {
    candidates = await listCandidates();
    for (const c of candidates) if (!c.error) picked[c.name] = true;
  }
  async function adoptPicked() {
    const todo = candidates.filter((c) => picked[c.name] && !c.error);
    if (!todo.length) return;
    adopting = true;
    let failed = 0;
    for (const [i, c] of todo.entries()) {
      adoptProgress = `${i + 1} / ${todo.length}: ${c.name}`;
      try {
        const id = await adoptContainer(c, true);
        await startStack(id);
        adoptedCount++;
      } catch (e: any) {
        failed++;
        toast(`${c.name}: ${e.message}`, { kind: 'error' });
      }
    }
    adopting = false;
    adoptProgress = '';
    toast(failed ? `Adopted ${todo.length - failed}, ${failed} failed` : `Adopted ${todo.length} container${todo.length === 1 ? '' : 's'}`, { kind: failed ? 'error' : 'success' });
    await loadCandidates();
  }

  // ---- navigation ----
  let finishing = false;
  // Every step that saves or looks something up awaits, and a button that
  // looks idle while it does invites a second click -- which used to run next()
  // twice and step straight past "Already running", the slowest one to load.
  // One flag guards re-entry and drives the spinner, so the button is never
  // both busy and idle-looking.
  let navigating = false;
  async function next() {
    if (navigating) return;
    navigating = true;
    try {
      if (step === 1 && !(await saveEngine())) return;
      if (step === 2 && !(await saveStorage())) return;
      // Refresh: something may have been started since the wizard opened.
      if (step === 3) await loadCandidates();
      step = Math.min(step + 1, STEPS.length - 1);
      if (step === ADOPT_STEP && !candidates.length) step = ADOPT_STEP + 1; // nothing to adopt
    } finally {
      navigating = false;
    }
  }
  function back() {
    let prev = Math.max(step - 1, 0);
    // Stepping back into the adopt screen when it was skipped forwards would
    // land on a step the indicator does not show, with nothing on it.
    if (prev === ADOPT_STEP && !candidates.length) prev = Math.max(prev - 1, 0);
    step = prev;
  }
  async function finish() {
    finishing = true;
    try {
      await fetch('/api/setup/state', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ done: true }),
      });
    } catch {}
    finishing = false;
    dispatch('done');
  }

  onMount(() => {
    loadChecks();
    loadStorage();
    loadCatalog();
    // Up front, not on the way into the step: the indicator has to know from
    // the first screen whether there is an "Already running" step at all.
    loadCandidates();
  });

  const STATUS: Record<string, { icon: string; cls: string }> = {
    ok: { icon: 'check', cls: 'text-fjord-success' },
    warn: { icon: 'alert', cls: 'text-fjord-warning' },
    fail: { icon: 'alert', cls: 'text-fjord-danger' },
    unknown: { icon: 'alert', cls: 'text-fjord-fg-dim' },
  };
</script>

<!-- Centred by m-auto, not items-center: flex centring pushes content taller
     than the screen off the TOP, where overflow-y cannot scroll to it -- the
     doctor list outgrew the viewport and its first checks were unreachable. -->
<div class="h-screen flex bg-fjord-bg text-fjord-fg-body p-6 overflow-y-auto">
  <div class="w-full max-w-2xl m-auto">
    <!-- step indicator -->
    <div class="flex items-center gap-2 mb-6">
      {#each visibleSteps as s, n}
        <button
          on:click={() => s.i < step && (step = s.i)}
          class="flex items-center gap-2 text-xs font-medium {s.i === step ? 'text-fjord-fg' : s.i < step ? 'text-fjord-fg-muted hover:text-fjord-fg-body' : 'text-fjord-fg-faint'}"
          disabled={s.i > step}
        >
          <span class="w-5 h-5 rounded-full flex items-center justify-center text-[10px] {s.i === step ? 'bg-fjord-accent text-white' : s.i < step ? 'bg-fjord-border text-fjord-fg-secondary' : 'border border-fjord-border'}"
            >{#if s.i < step}<Icon name="check" size={11} />{:else}{n + 1}{/if}</span
          >
          {s.label}
        </button>
        {#if n < visibleSteps.length - 1}<span class="flex-1 h-px bg-fjord-border"></span>{/if}
      {/each}
    </div>

    <div class="bg-fjord-card border border-fjord-border rounded-xl p-7 shadow-2xl">
      {#if step === 0}
        <h2 class="text-2xl font-bold text-fjord-fg mb-2">Welcome to fjord</h2>
        <p class="text-sm text-fjord-fg-muted mb-5">
          An app store for your own host. Pick an app, answer a couple of questions, and it runs as a
          stack you can see, update, and remove. This takes about two minutes and nothing here is final.
        </p>
        <div class="flex items-center justify-between mb-2">
          <h3 class="text-sm font-semibold text-fjord-fg-secondary">Host readiness{#if engineName} · {engineName}{/if}</h3>
          <!-- Fixes are applied in a root shell outside fjord; re-run without leaving the page. -->
          <button
            on:click={loadChecks}
            disabled={checksLoading}
            class="flex items-center gap-1.5 text-xs font-medium text-fjord-fg-muted hover:text-fjord-fg py-1 px-2.5 rounded-md border border-fjord-border hover:border-fjord-accent/40 transition-colors disabled:opacity-40"
            ><Icon name="refresh" size={13} /> Re-check</button
          >
        </div>
        {#if checksLoading}
          <div class="flex items-center gap-2 text-sm text-fjord-fg-dim"><Spinner size={14} /> Checking the host…</div>
        {:else if checks.length === 0}
          <p class="text-sm text-fjord-fg-dim">No checks reported.</p>
        {:else}
          <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border">
            {#each checks as c (c.id)}
              <div class="flex items-start gap-3 px-4 py-2.5">
                <Icon name={STATUS[c.status]?.icon || 'alert'} size={15} class="shrink-0 mt-0.5 {STATUS[c.status]?.cls || ''}" />
                <div class="min-w-0 flex-1">
                  <div class="text-sm text-fjord-fg-body">{c.name}{#if c.detail}<span class="text-fjord-fg-dim"> · {c.detail}</span>{/if}</div>
                  {#if c.why}<div class="text-xs text-fjord-fg-dim mt-0.5">{c.why}</div>{/if}
                  {#if c.status !== 'ok' && c.fix}<div class="mt-1.5"><FixSnippet fix={c.fix} /></div>{/if}
                </div>
              </div>
            {/each}
          </div>
          {#if failing.length}
            <p class="text-xs text-fjord-warning mt-3">
              {failing.length} check{failing.length === 1 ? '' : 's'} failing. Installs on this engine will not work until fixed;
              the same list lives on the System page, so you can carry on and come back to it.
            </p>
          {/if}
        {/if}
      {:else if step === 1}
        <h2 class="text-2xl font-bold text-fjord-fg mb-2">Engine</h2>
        <p class="text-sm text-fjord-fg-muted mb-5">
          The runtime new apps are installed on. Both run the same images as FreeBSD jails; each install can still
          pick the other, and a stack's engine is fixed once installed. Change the default any time under
          Settings → Engines.
        </p>
        <div class="space-y-2 mb-2">
          {#each engines as e (e.name)}
            <label
              class="flex items-start gap-3 p-3 rounded-xl border transition-colors {e.enabled
                ? engineChoice === e.name
                  ? 'border-fjord-accent bg-fjord-accent/10 cursor-pointer'
                  : 'border-fjord-border hover:border-fjord-neutral cursor-pointer'
                : 'border-fjord-border opacity-60 cursor-not-allowed'}"
            >
              <input type="radio" name="engine" value={e.name} bind:group={engineChoice} disabled={!e.enabled} class="mt-1 accent-fjord-accent" />
              <span class="flex items-center justify-center w-8 h-8 rounded-lg bg-fjord-bg border border-fjord-border shrink-0 text-fjord-fg-secondary"
                ><EngineMark engine={e.name} size={18} /></span
              >
              <span class="min-w-0">
                <span class="flex items-center gap-2">
                  <span class="font-semibold text-fjord-fg">{e.name}</span>
                  {#if !e.available}<span class="text-[10px] font-medium px-1.5 py-0.5 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-dim">not installed</span>{/if}
                </span>
                <span class="block text-xs text-fjord-fg-muted mt-0.5">{e.description}</span>
                {#if !e.available && e.reason}<span class="block text-xs text-fjord-fg-dim mt-0.5">{e.reason} — the System page shows how to install it.</span>{/if}
                {#if e.warning}<span class="flex items-start gap-1.5 text-xs text-fjord-warning mt-1"><Icon name="alert" size={12} class="shrink-0 mt-0.5" /> {e.warning}</span>{/if}
              </span>
            </label>
          {/each}
        </div>
        {#if !engines.some((e) => e.enabled)}
          <p class="text-sm text-fjord-warning">No engine is available yet — install one (see the readiness checks) and restart fjordd.</p>
        {/if}
      {:else if step === 2}
        <h2 class="text-2xl font-bold text-fjord-fg mb-2">Storage</h2>
        <p class="text-sm text-fjord-fg-muted mb-5">
          Where apps keep their data. Each app gets its own folder under this location. Pick a disk with room;
          you can add more locations later under Settings → Storage.
        </p>
        <label class="text-sm font-semibold text-fjord-fg-secondary" for="appdata">App data location</label>
        <div class="flex items-center gap-2 mt-1.5 mb-1">
          <input
            id="appdata"
            bind:value={appData}
            placeholder={appDataDefault}
            class="flex-1 min-w-0 bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent"
          />
          {#if hostMode}
            <button on:click={() => (pickDir = true)} class="px-3 py-2 rounded-md text-sm font-medium bg-fjord-border hover:bg-fjord-accent hover:text-white transition-colors">Browse…</button>
          {/if}
        </div>
        <p class="text-xs text-fjord-fg-dim mb-6">Leave it empty to use <span class="font-mono">{appDataDefault}</span>.</p>

        <div class="border border-fjord-border px-4 py-3 flex items-center gap-3 {homelab && presets.length ? 'rounded-t-xl' : 'rounded-xl'}">
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium text-fjord-fg">Homelab presets</span>
              <span class="text-[10px] font-semibold uppercase tracking-wide text-fjord-success">suggested</span>
            </div>
            <div class="text-xs text-fjord-fg-dim">
              Movies, TV, Music, Downloads, Books, Photos as ready-made folder sets. Apps that want one of those
              folders pick it up at install instead of asking. Turn it off if this host isn't a media box.
            </div>
          </div>
          <button
            on:click={toggleHomelab}
            aria-label={homelab ? 'Disable Homelab presets' : 'Enable Homelab presets'}
            class="shrink-0 relative w-11 h-6 rounded-full transition-colors {homelab ? 'bg-fjord-accent' : 'bg-fjord-border'}"
          >
            <span class="absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white shadow transition-transform {homelab ? 'translate-x-5' : ''}"></span>
          </button>
        </div>

        {#if homelab && presets.length}
          <div class="border border-fjord-border border-t-0 rounded-b-xl -mt-px px-4 pt-3 pb-4">
            <p class="text-xs text-fjord-fg-dim mb-3">
              Where these folders live on this host. Add the ones you have (local paths or NFS/SMB);
              leave the rest empty. You can edit them later under Settings → Storage.
            </p>
            <div class="divide-y divide-fjord-border/60">
              {#each presets as pr (pr.name)}
                <div class="py-3 first:pt-0 last:pb-0">
                  <div class="flex items-center gap-2 mb-1.5">
                    <Icon name={presetIcon(pr.name)} size={14} class="shrink-0 text-fjord-fg-dim" />
                    <span class="text-sm font-medium text-fjord-fg-secondary">{pr.name}</span>
                  </div>
                  <FolderRows bind:folders={presetFolders[pr.name]} />
                </div>
              {/each}
            </div>
          </div>
        {/if}
      {:else if step === 3}
        <h2 class="text-2xl font-bold text-fjord-fg mb-2">Catalog</h2>
        <p class="text-sm text-fjord-fg-muted mb-5">
          Where apps come from. A catalog is a URL publishing an app list, icons and install manifests.
          The daemonless catalog is the default; your own, or another fjord's <span class="font-mono">/catalog</span>, works the same way.
        </p>
        {#if catalogs.length}
          <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border mb-3">
            {#each catalogs as c (c.id)}
              <div class="flex items-center gap-3 px-4 py-2.5">
                {#if c.icon}
                  <img src={c.icon} alt="" class="shrink-0 w-7 h-7 rounded-md object-contain bg-fjord-inset" />
                {:else}
                  <Icon name="check" size={15} class="shrink-0 text-fjord-success" />
                {/if}
                <div class="min-w-0 flex-1">
                  <div class="text-sm text-fjord-fg">{c.name} <span class="text-[11px] text-fjord-fg-dim">{c.apps} apps</span></div>
                  <div class="text-xs text-fjord-fg-dim font-mono truncate">{c.url}</div>
                </div>
                {#if confirmRemove === c.id}
                  <button on:click={() => { confirmRemove = ''; removeCatalog(c); }} class="shrink-0 text-xs px-2 py-1 rounded bg-fjord-danger hover:bg-fjord-danger-hover text-white">Remove</button>
                  <button on:click={() => (confirmRemove = '')} class="shrink-0 text-xs px-2 py-1 rounded text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
                {:else}
                  <button
                    on:click={() => (confirmRemove = c.id)}
                    title="Remove this catalog"
                    class="shrink-0 text-fjord-fg-dim hover:text-fjord-danger transition-colors"><Icon name="trash" size={14} /></button
                  >
                {/if}
              </div>
            {/each}
          </div>
          <p class="text-xs text-fjord-fg-dim mb-4">{totalApps} apps ready. The default is the daemonless catalog; remove it or add your own below — all of this is editable later under Settings → Catalogs.</p>
        {:else}
          <p class="text-xs text-fjord-warning mb-4">No catalog configured — the store will be empty until one is added.</p>
        {/if}
        <label class="text-sm font-semibold text-fjord-fg-secondary" for="caturl">Add another catalog</label>
        <div class="flex items-center gap-2 mt-1.5">
          <input
            id="caturl"
            bind:value={catalogURL}
            placeholder={defaultCatalogURL || 'https://…/v1/<source>'}
            class="flex-1 min-w-0 bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent"
          />
          <button
            on:click={() => addCatalog()}
            disabled={addingCatalog || !catalogURL.trim()}
            class="flex items-center gap-2 px-4 py-2 rounded-md text-sm font-medium bg-fjord-accent hover:bg-fjord-accent-hover text-white disabled:opacity-50"
            >{#if addingCatalog}<Spinner size={13} /> Fetching…{:else}Add{/if}</button
          >
        </div>
        {#if !hasDefault}
          <button
            on:click={() => addCatalog(defaultCatalogURL)}
            disabled={addingCatalog}
            class="mt-3 flex items-center gap-1.5 text-xs font-medium text-fjord-fg-muted hover:text-fjord-fg py-1 px-2.5 rounded-md border border-fjord-border hover:border-fjord-accent/40 transition-colors disabled:opacity-40"
            ><Icon name="plus" size={12} /> Re-add the daemonless catalog</button
          >
        {/if}
      {:else if step === 4}
        <h2 class="text-2xl font-bold text-fjord-fg mb-2">Already running on this host</h2>
        <p class="text-sm text-fjord-fg-muted mb-5">
          These containers and jails were started outside fjord. Adopting one turns what the engine recorded
          into a stack — same image, mounts, network address and name — and starts it in place of the old one.
          Data stays where it is. Untick anything you'd rather leave alone; you can adopt later from the Stacks
          page.
        </p>
        <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border mb-4 max-w-2xl">
          {#each candidates as c (c.engine + ':' + c.id)}
            <label class="flex items-center gap-3 px-4 py-2.5 {c.error ? 'opacity-60' : 'cursor-pointer hover:bg-fjord-border/40'}">
              <input type="checkbox" bind:checked={picked[c.name]} disabled={!!c.error || adopting} class="accent-fjord-accent" />
              <EngineMark engine={c.engine} size={14} />
              <span class="min-w-0 flex-1">
                <span class="text-sm text-fjord-fg-body">{c.name}</span>
                <span class="block text-xs text-fjord-fg-dim font-mono truncate">{c.image}</span>
                {#if c.error}<span class="block text-xs text-fjord-danger">{c.error}</span>{/if}
                {#each c.notes || [] as n}<span class="block text-xs text-fjord-warning">{n}</span>{/each}
              </span>
              <span class="text-[10px] font-semibold uppercase tracking-wide {c.state === 'running' ? 'text-fjord-success' : 'text-fjord-fg-dim'}">{c.state}</span>
            </label>
          {/each}
        </div>
        <button
          on:click={adoptPicked}
          disabled={adopting || !candidates.some((c) => picked[c.name] && !c.error)}
          class="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-fjord-accent hover:bg-fjord-accent-hover text-white disabled:opacity-50"
          >{#if adopting}<Spinner size={13} /> {adoptProgress}{:else}Adopt &amp; replace selected{/if}</button
        >
        {#if adoptedCount}
          <p class="text-xs text-fjord-success mt-3">{adoptedCount} adopted — they're on the Stacks page.</p>
        {/if}
      {:else}
        <h2 class="text-2xl font-bold text-fjord-fg mb-2">How it works</h2>
        <div class="space-y-3 mb-2">
          {#each [
            { icon: 'store', title: 'App Store', text: 'Browse, pick an app, press Install. The wizard asks only what it must; the defaults are chosen so the app works untouched.' },
            { icon: 'list', title: 'Stacks', text: 'Everything running, with logs, a shell, updates and delete. Each app is a compose file you can open and edit.' },
            { icon: 'drive', title: 'Volumes', text: 'App data lives in the location you just picked, one folder per app, plus any named volumes an app declares.' },
            { icon: 'settings', title: 'Settings', text: 'Storage, engines, catalogs, and the knobs you will not need on day one. Setup can be run again from Advanced.' },
          ] as g}
            <div class="flex items-start gap-3">
              <span class="shrink-0 w-8 h-8 rounded-lg bg-fjord-bg border border-fjord-border flex items-center justify-center text-fjord-accent"><Icon name={g.icon} size={16} /></span>
              <div>
                <div class="text-sm font-semibold text-fjord-fg">{g.title}</div>
                <div class="text-xs text-fjord-fg-muted">{g.text}</div>
              </div>
            </div>
          {/each}
        </div>
      {/if}

      <!-- footer -->
      <div class="flex items-center gap-3 mt-7 pt-5 border-t border-fjord-border">
        {#if step > 0}
          <button on:click={back} disabled={navigating} class="px-3 py-2 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg disabled:opacity-40">Back</button>
        {/if}
        <div class="flex-1"></div>
        {#if step < STEPS.length - 1}
          <button on:click={finish} disabled={finishing} class="px-3 py-2 rounded-lg text-sm font-medium text-fjord-fg-dim hover:text-fjord-fg-secondary">Skip setup</button>
          <button
            on:click={next}
            disabled={navigating || adopting}
            class="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-fjord-accent hover:bg-fjord-accent-hover text-white disabled:opacity-50"
            >{#if navigating}<Spinner size={13} />{/if}Continue <Icon name="chevron-right" size={14} /></button
          >
        {:else}
          <button
            on:click={finish}
            disabled={finishing}
            class="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-fjord-accent hover:bg-fjord-accent-hover text-white disabled:opacity-50"
            >{#if finishing}<Spinner size={13} />{/if}Open the App Store</button
          >
        {/if}
      </div>
    </div>
  </div>
</div>

{#if pickDir}
  <DirPicker start={appData || appDataDefault || '/'} on:select={(e) => { appData = e.detail; pickDir = false; }} on:close={() => (pickDir = false)} />
{/if}
