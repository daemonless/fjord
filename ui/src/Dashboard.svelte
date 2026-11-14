<script lang="ts">
  import { failedAction, problem } from './stackHealth';
  import { statusKey } from './staleNote';
  import { onMount, createEventDispatcher } from 'svelte';
  import Icon from './Icon.svelte';
  import EngineMark from './EngineMark.svelte';
  import EmptyState from './EmptyState.svelte';
  import Spinner from './Spinner.svelte';
  import { expandVars } from './expand';
  import { names, openBy, deviceTrust, openUrl, opensByName, setupUrl } from './names';
  import TrustMark from './TrustMark.svelte';
  import { serviceLinks } from './serviceLinks';
  import { restartShort, restartTitle, type StackRestart } from './restartPolicy';
  import { changeSentence } from './updateWords';
  import { readStacksView, writeStacksView, type StoreView } from './storeView';
  import {
    changesKeyFor,
    isUpdatableFleetStack,
    massUpdateRows,
    pickedMassRows,
    shortDigest,
    type DetailedChanges,
  } from './massUpdate';

  type ContainerStatus = {
    name: string;
    state: string;
    ports?: { hostPort: number; containerPort: number; protocol?: string }[];
    address?: string; // the container's own IP on an attachable network
  };
  type StackStatus = { state: string; containers: ContainerStatus[] };
  type Stack = { name: string; displayName?: string; icon?: string; busy?: string; compose?: string; env?: string; services?: { name: string; address?: string }[]; status?: StackStatus; state?: { origin?: { type?: string; app_id?: string }; engine?: string; desired_state?: string }; restart?: StackRestart };
  export let stacks: Stack[] = [];
  export let loaded = true;
  // Set when the list did not arrive: 0 = fjord did not answer, else its HTTP status.
  export let failed: number | null = null;
  // Fleet-wide update state, fetched once by the app shell (server-cached).
  export let fleet: Record<string, any> = {};
  export let fleetRefreshing = false;
  export let updatingAll = false;
  export let updateAllOpen = false;

  const dispatch = createEventDispatcher();
  // One line that says whether anything needs you: the first stack meant to
  // run that isn't, with a way to it -- or that all is well.
  $: problems = stacks.map((s) => ({ s, p: problem(s) })).filter((x) => x.p);
  $: stoppedByYou = stacks.filter((s) => s.state?.desired_state === 'stopped').length;

  import { appIcons, loadAppIcons, iconFor, tile } from './appIcons';
  // Resolve a stack's catalog icon: by its stored app_id first (stable), then by
  $: iconOf = (s: Stack) => iconFor($appIcons, s);
  let info: Record<string, { tag?: string; link?: string; byName?: boolean; more?: string[] }> = {};

  const DOT: Record<string, string> = {
    running: 'bg-fjord-success',
    partial: 'bg-fjord-warning',
    stopped: 'bg-fjord-neutral',
    checking: 'bg-fjord-neutral animate-pulse', // not known yet is not broken
    unknown: 'bg-fjord-warning',
  };
  // The state as a word; inside a function so the catalog carries it.
  function stateWord(k: string): string {
    switch (k) {
      case 'running': return 'Running';
      case 'stopped': return 'Stopped';
      case 'partial': return 'Partial';
      case 'checking': return 'Checking';
      case 'unknown': return 'Unknown';
      default: return k;
    }
  }
  const label = (s?: StackStatus) => stateWord(statusKey(s));
  const running = (s?: StackStatus) => s?.state === 'running' || s?.state === 'partial';
  function busyWord(k?: string): string {
    switch (k) {
      case 'installing': return 'Installing…';
      case 'up': return 'Starting…';
      case 'update': return 'Updating…';
      case 'restart': return 'Restarting…';
      case 'down': return 'Stopping…';
      case 'deleting': return 'Deleting…';
      default: return '';
    }
  }

  $: runningCount = stacks.filter((s) => running(s.status)).length;
  $: updateCount = stacks.filter(
    (s) => fleet[s.name]?.state === 'available' || fleet[s.name]?.state === 'upgrade'
  ).length;
  $: availableStacks = stacks.filter((s) => isUpdatableFleetStack(fleet[s.name]) && !s.busy);
  $: updatingStacks = stacks.filter((s) => s.busy === 'update');


  function firstImage(compose: string): string {
    const m = compose.match(/^\s*image:\s*["']?([^\s"'#]+)/m);
    return m ? m[1] : '';
  }
  function imgTag(image: string): string {
    const b = image.includes('@') ? image.slice(0, image.indexOf('@')) : image;
    const s = b.lastIndexOf('/'), c = b.lastIndexOf(':');
    return c > s ? b.slice(c + 1) : 'latest';
  }


  // Parse .env text into a var map (KEY=value per line).
  function envMap(env: string): Record<string, string> {
    const vars: Record<string, string> = {};
    for (const line of (env || '').split('\n')) {
      const eq = line.indexOf('=');
      if (eq > 0) vars[line.slice(0, eq).trim()] = line.slice(eq + 1).trim();
    }
    return vars;
  }

  // The list carries compose + .env, so tags and links come from the data
  // already here -- one request for the whole dashboard, not one per tile.
  $: {
    const next: Record<string, { tag?: string; link?: string; byName?: boolean; more?: string[] }> = {};
    for (const s of stacks) {
      // Resolve ${VAR:-default} tags (e.g. immich's ${IMMICH_TAG:-latest})
      // before splitting, or the tag reads back as "-latest}".
      const img = expandVars(firstImage(s.compose || ''), envMap(s.env || ''));
      // A stack of several apps: the rest are named in the "+N" beside the link.
      const all = serviceLinks(s.compose || '', envMap(s.env || ''), s.services || [], location.hostname);
      next[s.name] = {
        tag: img ? imgTag(img) : '',
        link: openUrl(s, $names, $openBy, $deviceTrust),
        byName: opensByName(s, $names, $openBy),
        more: all.length > 1 ? all.map((l) => `${l.service}  ${l.url.replace(/^https?:\/\//, '')}`) : [],
      };
    }
    info = next;
  }

  onMount(() => {
    loadAppIcons();
  });

  // Containers on this host that Adopt could take over, asked once the list
  // is in (the list comes first). Hidden is per browser and by name: a new
  // container brings the line back.
  const HIDDEN = 'fjord.adoptHidden';
  let adoptable: string[] = [];
  let hidden: string[] = [];
  let askedAdoptable = false;
  try {
    hidden = JSON.parse(localStorage.getItem(HIDDEN) || '[]');
  } catch {}
  $: if (loaded && stacks.length && !askedAdoptable) {
    askedAdoptable = true;
    fetch('/api/adopt/names')
      .then((r) => (r.ok ? r.json() : { names: [] }))
      .then((j) => (adoptable = j.names || []))
      .catch(() => {});
  }
  $: unseen = adoptable.filter((n) => !hidden.includes(n));
  function hideAdoptable() {
    hidden = adoptable;
    try {
      localStorage.setItem(HIDDEN, JSON.stringify(adoptable));
    } catch {}
  }

  const act = (name: string, action: string) => dispatch('action', { name, action });

  // Update all review panel: what each behind stack's update changes before it runs.
  let unpickedStacks: Record<string, boolean> = {};
  let changes: Record<string, DetailedChanges | 'loading' | 'none'> = {};
  let changesOpen: Record<string, boolean> = {};
  $: fallbackTags = Object.fromEntries(Object.entries(info).map(([k, v]) => [k, v.tag]));
  $: massRows = massUpdateRows(stacks, fleet, fallbackTags);
  $: pickedRows = pickedMassRows(massRows, unpickedStacks);
  $: if (!massRows.length && updateAllOpen) updateAllOpen = false;
  $: if (updateAllOpen) {
    for (const row of massRows) {
      for (const s of row.services) {
        const k = changesKeyFor(row.stack, s);
        if (changes[k]) continue;
        changes[k] = 'loading';
        fetch(`/api/stacks/${encodeURIComponent(row.stack)}/changes?service=${encodeURIComponent(s.service)}`)
          .then((r) => (r.ok ? r.json() : 'none'))
          .catch(() => 'none')
          .then((d) => (changes = { ...changes, [k]: d }));
      }
    }
  }
  function toggleUpdateAll() {
    if (!updateAllOpen) unpickedStacks = {};
    updateAllOpen = !updateAllOpen;
  }

  let view: StoreView = readStacksView();
  function setView(next: StoreView) {
    view = next;
    writeStacksView(next);
  }
</script>

<div class="h-full flex flex-col">
  <div class="flex items-center justify-between mb-4 shrink-0 flex-wrap gap-2">
    <div>
      <h2 class="text-2xl font-bold text-fjord-fg">Stacks</h2>
      <div class="text-sm text-fjord-fg-dim">
        {#if !loaded}
          {failed !== null ? 'Not loaded' : 'Loading…'}
        {:else if !stacks.length}
          No stacks yet
        {:else if problems.length}
          <span class="text-fjord-danger font-medium">{problems[0].s.displayName || problems[0].s.name} {problems[0].p}</span>
          — <button on:click={() => dispatch('select', problems[0].s.name)} class="text-fjord-accent hover:underline">View</button>
          {#if problems.length > 1}<span> · and {problems.length - 1} more</span>{/if}
        {:else}
          <span class="text-fjord-success">All {runningCount} running</span>{#if stoppedByYou}
            · {stoppedByYou} stopped{/if}
        {/if}{#if updateCount}
          · <span class="text-fjord-warning">{updateCount} update{updateCount === 1 ? '' : 's'} available</span>{/if}{#if updatingStacks.length}
          · <span class="text-fjord-accent">{updatingStacks.length} updating</span>{/if}
      </div>
    </div>
    <div class="flex items-center gap-2 flex-wrap">
      {#if stacks.length > 0}
        <div class="inline-flex rounded-md border border-fjord-border bg-fjord-card p-0.5" role="group" aria-label="Stacks view">
          <button
            type="button"
            aria-pressed={view === 'cards'}
            title="Cards view"
            on:click={() => setView('cards')}
            class="flex items-center gap-1.5 px-2.5 py-1.5 rounded text-xs font-medium transition-colors {view === 'cards' ? 'bg-fjord-accent text-white' : 'text-fjord-fg-muted hover:text-fjord-fg'}"
            ><Icon name="grid" size={13} /> Cards</button
          >
          <button
            type="button"
            aria-pressed={view === 'list'}
            title="List view"
            on:click={() => setView('list')}
            class="flex items-center gap-1.5 px-2.5 py-1.5 rounded text-xs font-medium transition-colors {view === 'list' ? 'bg-fjord-accent text-white' : 'text-fjord-fg-muted hover:text-fjord-fg'}"
            ><Icon name="list" size={13} /> List</button
          >
        </div>
      {/if}
      {#if availableStacks.length > 0 || updatingAll || updatingStacks.length > 0}
        <button
          on:click={toggleUpdateAll}
          disabled={updatingAll || availableStacks.length === 0}
          title={availableStacks.length
            ? `Review and update ${availableStacks.map((s) => s.displayName || s.name).join(', ')}`
            : 'Updating in the background'}
          class="flex items-center gap-2 bg-fjord-warning/15 hover:bg-fjord-warning/25 text-fjord-warning border border-fjord-warning/40 font-medium py-2 px-4 rounded-lg text-sm disabled:opacity-50"
          ><Icon name="update" size={14} /> {updatingAll || (availableStacks.length === 0 && updatingStacks.length > 0)
            ? 'Updating…'
            : `Update all (${availableStacks.length})`}</button
        >
      {/if}
      <button
        on:click={() => dispatch('adopt')}
        title="Turn containers started outside fjord into stacks"
        class="flex items-center gap-2 bg-fjord-border hover:bg-fjord-accent hover:text-white text-fjord-fg-secondary font-medium py-2 px-4 rounded-lg text-sm"
        ><Icon name="download" size={14} /> Adopt existing…</button
      >
      <button
        on:click={() => dispatch('new')}
        class="flex items-center gap-2 bg-fjord-accent hover:bg-fjord-accent-hover text-white font-medium py-2 px-4 rounded-lg text-sm"
        ><Icon name="plus" size={14} /> New Stack</button
      >
    </div>
  </div>

  <div class="flex-1 overflow-y-auto">
    {#if !loaded && failed !== null}
      <EmptyState
        icon="alert"
        title="Stacks Did Not Load"
        description={failed ? `This page could not get the list of stacks: fjord answered ${failed}.` : 'This page could not get the list of stacks: fjord did not answer.'}
        actionLabel="Try Again"
        on:action={() => dispatch('retry')}
      />
    {:else if !loaded}
      <div class="h-full flex items-center justify-center gap-2 text-sm text-fjord-fg-dim">
        <Spinner size={16} /> Loading your stacks…
      </div>
    {:else if stacks.length === 0}
      <EmptyState
        icon="stack"
        title="No Stacks"
        description="Install an app from the store, adopt the containers already running on this host, or create a stack from a compose file."
        actionLabel="Open App Store"
        secondaryLabel="Adopt existing containers"
        on:action={() => dispatch('store')}
        on:secondary={() => dispatch('adopt')}
      />
    {:else}
      {#if unseen.length}
        <div class="flex items-center gap-3 mb-3 px-4 py-2.5 rounded-xl border border-fjord-border bg-fjord-card text-sm text-fjord-fg-secondary">
          <Icon name="download" size={14} />
          <span class="flex-1 min-w-0"
            >{#if adoptable.length === 1}1 container on this host isn't run by fjord yet.{:else}{adoptable.length} containers on this host aren't run by fjord yet.{/if}</span
          >
          <button on:click={() => dispatch('adopt')} class="text-fjord-accent hover:underline font-medium shrink-0">Adopt…</button>
          <button
            on:click={hideAdoptable}
            title="Hide until another one turns up"
            aria-label="Hide"
            class="p-1 rounded text-fjord-fg-dim hover:text-fjord-fg hover:bg-fjord-border shrink-0"><Icon name="close" size={12} /></button
          >
        </div>
      {/if}
      {#if updateAllOpen && massRows.length > 0}
        <div class="mb-4 px-4 py-3 rounded-xl bg-fjord-card border border-fjord-border text-sm text-fjord-fg-body">
          <div class="overflow-x-auto">
            <table class="w-full text-xs">
              <thead class="text-fjord-fg-dim">
                <tr>
                  {#if massRows.length > 1}<th class="w-6 pb-1.5"></th>{/if}
                  <th class="text-left font-medium pb-1.5">Stack</th>
                  <th class="text-left font-medium pb-1.5">Tag</th>
                  <th class="text-left font-medium pb-1.5">Change</th>
                </tr>
              </thead>
              <tbody>
                {#each massRows as row (row.stack)}
                  {#each row.services as s, i (s.service)}
                    {@const k = changesKeyFor(row.stack, s)}
                    {@const c = changes[k]}
                    <tr class="border-t border-fjord-border/60 align-top">
                      {#if massRows.length > 1}
                        <td class="py-1.5 pr-1">
                          {#if i === 0}
                            <input
                              type="checkbox"
                              checked={!unpickedStacks[row.stack]}
                              on:change={(e) => (unpickedStacks = { ...unpickedStacks, [row.stack]: !e.currentTarget.checked })}
                              aria-label="Update {row.label}"
                              class="accent-fjord-accent"
                            />
                          {/if}
                        </td>
                      {/if}
                      <td class="py-1.5 pr-3 font-medium text-fjord-fg whitespace-nowrap">
                        {#if i === 0}
                          <button
                            on:click={() => dispatch('select', row.stack)}
                            class="hover:underline text-left font-medium text-fjord-fg">{row.label}</button
                          >
                        {/if}
                        {#if row.showServiceName}
                          <span class="font-mono text-fjord-fg-dim {i === 0 ? 'ml-1.5' : ''}">{s.service}</span>
                        {/if}
                      </td>
                      <td class="py-1.5 pr-3 font-mono text-fjord-fg-secondary whitespace-nowrap"
                        >{s.newTag ? `${s.tag ?? ''} → ${s.newTag}` : (s.tag ?? '')}</td
                      >
                      <td class="py-1.5 text-fjord-fg-secondary">
                        {#if c === 'loading' || !c}
                          <span class="text-fjord-fg-dim" title="{shortDigest(s.running) || 'local'} → {shortDigest(s.latest)}"
                            >Reading what changed…</span
                          >
                        {:else if c !== 'none' && changeSentence(c)}
                          {@const n = (c.changed?.length ?? 0) + (c.added?.length ?? 0) + (c.removed?.length ?? 0)}
                          <span title="{shortDigest(s.running) || 'local'} → {shortDigest(s.latest)}">{changeSentence(c)}</span>
                          {#if c.packages && n}
                            <button
                              on:click={() => (changesOpen = { ...changesOpen, [k]: !changesOpen[k] })}
                              class="ml-1 text-fjord-accent hover:underline">{changesOpen[k] ? 'hide' : 'show'}</button
                            >
                          {/if}
                          {#if changesOpen[k]}
                            <div class="mt-1 font-mono text-[11px] leading-5 max-h-48 overflow-y-auto">
                              {#each c.changed ?? [] as p}<div>{p.name} <span class="text-fjord-fg-dim">{p.from} →</span> {p.to}</div>{/each}
                              {#each c.added ?? [] as p}<div class="text-fjord-success">+ {p.name} {p.version}</div>{/each}
                              {#each c.removed ?? [] as p}<div class="text-fjord-danger">− {p.name} {p.version}</div>{/each}
                            </div>
                          {/if}
                        {:else}
                          <span title="{shortDigest(s.running) || 'local'} → {shortDigest(s.latest)}">new build</span>
                        {/if}
                      </td>
                    </tr>
                  {/each}
                {/each}
              </tbody>
            </table>
          </div>
          <div class="flex items-center gap-3 mt-3">
            <span class="flex-1 text-xs text-fjord-fg-dim">
              {#if !pickedRows.length}
                Tick the stacks to update.
              {:else if pickedRows.length === 1}
                Updates {pickedRows[0].label} in the background.
              {:else}
                Updates {pickedRows.length} stacks one at a time in the background.
              {/if}
            </span>
            <button
              on:click={() => (updateAllOpen = false)}
              class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg transition-colors"
              >Cancel</button
            >
            <button
              on:click={() => {
                const names = pickedRows.map((r) => r.stack);
                updateAllOpen = false;
                dispatch('update-all', names);
              }}
              disabled={!pickedRows.length || updatingAll}
              class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-accent text-white hover:bg-fjord-accent-hover transition-colors disabled:opacity-40"
              >{!pickedRows.length
                ? 'Update'
                : pickedRows.length === 1
                  ? `Update ${pickedRows[0].label}`
                  : `Update ${pickedRows.length} stacks`}</button
            >
          </div>
        </div>
      {/if}
      {#if view === 'cards'}
        <div class="grid grid-cols-[repeat(auto-fill,minmax(320px,1fr))] gap-4 pb-8">
          {#each stacks as s (s.name)}
            {@const u = fleet[s.name]}
            <div
              role="button"
              tabindex="0"
              class="bg-fjord-card border border-fjord-border rounded-xl p-5 hover:border-fjord-accent/50 focus:outline-none focus:border-fjord-accent transition-colors flex flex-col gap-3 cursor-pointer"
              on:click={() => dispatch('select', s.name)}
              on:keydown={(e) => {
                if (e.target === e.currentTarget && (e.key === 'Enter' || e.key === ' ')) {
                  e.preventDefault();
                  dispatch('select', s.name);
                }
              }}
            >
              <div class="flex items-start gap-3.5">
                <div class="relative shrink-0">
                  {#if iconOf(s)}
                    <img src={iconOf(s)} alt="" draggable="false" class="w-11 h-11 rounded-lg object-contain bg-fjord-bg p-1" />
                  {:else}
                    <div
                      class="w-11 h-11 rounded-lg flex items-center justify-center font-bold text-fjord-fg text-base"
                      style="background:{tile(s.displayName || s.name)}"
                    >
                      {(s.displayName || s.name)[0]?.toUpperCase()}
                    </div>
                  {/if}
                  {#if s.state?.engine === 'podman'}
                    <span
                      class="absolute -bottom-1 -right-1 w-[18px] h-[18px] rounded-full bg-white flex items-center justify-center ring-2 ring-fjord-card"
                      title="Runs on the podman engine"
                    >
                      <EngineMark engine="podman" size={15} />
                    </span>
                  {:else if s.state?.engine}
                    <span
                      class="absolute -bottom-1 -right-1 w-4 h-4 rounded-full bg-fjord-accent text-white flex items-center justify-center ring-2 ring-fjord-card"
                      title="Runs on the {s.state.engine} engine"
                    >
                      <EngineMark engine={s.state.engine} size={10} strokeWidth={2.5} />
                    </span>
                  {/if}
                </div>
                <div class="min-w-0 flex-1">
                  <div class="text-fjord-fg font-semibold text-base truncate">{s.displayName || s.name}</div>
                  <div class="text-xs text-fjord-fg-dim font-mono truncate">{info[s.name]?.tag ? ':' + info[s.name].tag : ''}</div>
                </div>
                <div class="flex items-center gap-1.5 text-xs {s.busy ? 'text-fjord-accent' : 'text-fjord-fg-muted'} shrink-0">
                  {#if s.busy}
                    <Spinner size={11} class="text-fjord-accent" />
                    <span class="truncate">{busyWord(s.busy) || 'Busy…'}</span>
                  {:else}
                    <span class="w-2 h-2 rounded-full {DOT[label(s.status)]}"></span>
                    <span class="capitalize">{label(s.status)}</span>
                  {/if}
                </div>
              </div>

              <div class="flex items-center justify-between gap-2 text-xs min-h-[20px]">
                <div class="min-w-0 flex items-center gap-1.5">
                  {#if s.name === $names.provider && !$names.ready && $names.setupToken}
                    <a
                      href={setupUrl($names, stacks)}
                      target="_blank"
                      rel="noreferrer"
                      on:click|stopPropagation
                      class="text-xs font-semibold px-2.5 py-1 rounded-lg bg-fjord-accent text-white hover:bg-fjord-accent-hover"
                      >Finish setup</a
                    >
                  {:else if info[s.name]?.link}
                    {#if info[s.name]?.byName}<TrustMark {stacks} />{/if}
                    <a
                      href={info[s.name].link}
                      target="_blank"
                      rel="noreferrer"
                      on:click|stopPropagation
                      class="text-xs font-mono text-fjord-accent hover:underline truncate flex items-center gap-1 {running(s.status)
                        ? ''
                        : 'opacity-40 pointer-events-none'}"
                      ><span class="truncate">{(info[s.name]?.link || '').replace(/^https?:\/\//, '')}</span>
                      <Icon name="external" size={11} /></a
                    >
                    {#if info[s.name]?.more?.length}
                      <span class="text-[11px] text-fjord-fg-muted shrink-0" title={info[s.name].more?.join('\n')}
                        >+{(info[s.name].more?.length ?? 1) - 1} more apps</span
                      >
                    {/if}
                  {/if}
                </div>
                {#if s.restart}
                  <div class="flex items-center gap-1 text-xs text-fjord-fg-dim shrink-0" title={restartTitle(s.restart)}>
                    <Icon name={s.restart.onExit || !s.restart.policies.length ? 'restart' : 'power'} size={11} />
                    <span>{restartShort(s.restart)}</span>
                  </div>
                {/if}
              </div>

              <div class="mt-auto pt-3 border-t border-fjord-border/50 flex items-center justify-between gap-2">
                <div>
                  {#if s.busy === 'update'}
                    <span
                      class="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full text-fjord-accent bg-fjord-accent/10 border border-fjord-accent/30"
                      ><Spinner size={10} /> Updating…</span
                    >
                  {:else if failedAction(s)}
                    <span
                      class="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full text-fjord-danger bg-fjord-danger/10 border border-fjord-danger/30"
                      title={failedAction(s)?.message}><Icon name="alert" size={11} /> {failedAction(s)?.label}</span
                    >
                  {:else if !u && fleetRefreshing}
                    <span class="text-xs text-fjord-fg-faint">…</span>
                  {:else if u?.state === 'available'}
                    <span
                      class="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full text-fjord-warning bg-fjord-warning/10 border border-fjord-warning/30"
                      ><Icon name="arrow-up" size={11} /> Update</span
                    >
                  {:else if u?.state === 'upgrade'}
                    <span
                      class="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full text-fjord-warning bg-fjord-warning/10 border border-fjord-warning/30"
                      ><Icon name="arrow-up" size={11} /> {u.fromVersion ? `v${u.fromVersion} → ` : ''}v{u.toVersion}</span
                    >
                  {:else if u?.state === 'pinned'}
                    <span class="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full text-fjord-accent bg-fjord-accent/10"
                      ><Icon name="pin" size={11} /> Pinned</span
                    >
                  {:else if u?.state === 'current'}
                    <span class="inline-flex items-center gap-1 text-xs font-medium text-fjord-success"
                      ><Icon name="check" size={11} /> Up to date</span
                    >
                  {:else if u?.state === 'unknown'}
                    <span
                      class="inline-flex items-center gap-1 text-xs text-fjord-fg-dim cursor-help"
                      title={u.detail || 'Could not compare the local image with the registry'}><Icon name="help" size={11} /> Unknown</span
                    >
                  {/if}
                </div>
                <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
                <div class="flex gap-1.5 shrink-0" on:click|stopPropagation>
                  {#if running(s.status)}
                    <button
                      on:click={() => act(s.name, 'down')}
                      disabled={!!s.busy}
                      title={s.busy ? busyWord(s.busy) : 'Stop'}
                      class="w-8 h-8 flex items-center justify-center rounded-lg bg-fjord-bg border border-fjord-border text-fjord-fg-muted hover:bg-fjord-border hover:text-fjord-fg disabled:opacity-40"
                      ><Icon name="stop" size={13} /></button
                    >
                  {:else}
                    <button
                      on:click={() => act(s.name, 'up')}
                      disabled={!!s.busy}
                      title={s.busy ? busyWord(s.busy) : 'Start'}
                      class="w-8 h-8 flex items-center justify-center rounded-lg bg-fjord-bg border border-fjord-border text-fjord-fg-muted hover:bg-fjord-success/80 hover:text-white disabled:opacity-40"
                      ><Icon name="play" size={13} /></button
                    >
                  {/if}
                  <button
                    on:click={() => act(s.name, 'restart')}
                    disabled={!!s.busy}
                    title={s.busy ? busyWord(s.busy) : 'Restart'}
                    class="w-8 h-8 flex items-center justify-center rounded-lg bg-fjord-bg border border-fjord-border text-fjord-fg-muted hover:bg-fjord-border hover:text-fjord-fg disabled:opacity-40"
                    ><Icon name="restart" size={13} /></button
                  >
                  <button
                    on:click={() => act(s.name, 'update')}
                    disabled={!!s.busy}
                    title={s.busy ? busyWord(s.busy) : 'Update'}
                    class="w-8 h-8 flex items-center justify-center rounded-lg disabled:opacity-40 {u?.state === 'available'
                      ? 'bg-fjord-warning/10 border border-fjord-warning/40 text-fjord-warning'
                      : 'bg-fjord-bg border border-fjord-border text-fjord-fg-muted hover:bg-fjord-border hover:text-fjord-fg'}"
                    ><Icon name="update" size={13} /></button
                  >
                </div>
              </div>
            </div>
          {/each}
        </div>
      {:else}
      <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border bg-fjord-card">
        {#each stacks as s (s.name)}
          {@const u = fleet[s.name]}
          <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
          <div class="flex items-center gap-3.5 px-4 py-3 hover:bg-fjord-border/40 cursor-pointer" on:click={() => dispatch('select', s.name)}>
            <!-- icon, with the engine's mark overlaid -->
            <div class="relative shrink-0">
              {#if iconOf(s)}
                <img src={iconOf(s)} alt="" draggable="false" class="w-9 h-9 rounded-lg object-contain bg-fjord-bg p-0.5" />
              {:else}
                <div class="w-9 h-9 rounded-lg flex items-center justify-center font-bold text-fjord-fg text-sm" style="background:{tile(s.displayName || s.name)}">
                  {(s.displayName || s.name)[0]?.toUpperCase()}
                </div>
              {/if}
              {#if s.state?.engine === 'podman'}
                <span
                  class="absolute -bottom-1 -right-1 w-[18px] h-[18px] rounded-full bg-white flex items-center justify-center ring-2 ring-fjord-card"
                  title="Runs on the podman engine">
                  <EngineMark engine="podman" size={15} />
                </span>
              {:else if s.state?.engine}
                <span
                  class="absolute -bottom-1 -right-1 w-4 h-4 rounded-full bg-fjord-accent text-white flex items-center justify-center ring-2 ring-fjord-card"
                  title="Runs on the {s.state.engine} engine">
                  <EngineMark engine={s.state.engine} size={10} strokeWidth={2.5} />
                </span>
              {/if}
            </div>
            <!-- name + image -->
            <div class="min-w-0 flex-1">
              <div class="text-fjord-fg font-semibold text-sm truncate">{s.displayName || s.name}</div>
              <div class="text-xs text-fjord-fg-dim font-mono truncate">{info[s.name]?.tag ? ':' + info[s.name].tag : ''}</div>
            </div>
            <!-- status -->
            <div class="flex items-center gap-2 text-xs {s.busy ? 'text-fjord-accent' : 'text-fjord-fg-muted'} w-24 shrink-0">
              {#if s.busy}
                <Spinner size={11} class="text-fjord-accent" />
                <span class="truncate">{busyWord(s.busy) || 'Busy…'}</span>
              {:else}
                <span class="w-2 h-2 rounded-full {DOT[label(s.status)]}"></span>
                <span class="capitalize">{label(s.status)}</span>
              {/if}
            </div>
            <!-- restart policy, as the stack page's chip says it -->
            <div class="flex items-center gap-1 text-xs text-fjord-fg-dim w-36 shrink-0 truncate" title={restartTitle(s.restart)}>
              {#if s.restart}<Icon name={s.restart.onExit || !s.restart.policies.length ? 'restart' : 'power'} size={11} /><span class="truncate"
                  >{restartShort(s.restart)}</span
                >{/if}
            </div>
            <!-- update badge -->
            <div class="w-32 shrink-0 text-center">
              {#if s.busy === 'update'}
                <span class="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full text-fjord-accent bg-fjord-accent/10 border border-fjord-accent/30"
                  ><Spinner size={10} /> Updating…</span
                >
              {:else if failedAction(s)}
                <span
                  class="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full text-fjord-danger bg-fjord-danger/10 border border-fjord-danger/30"
                  title={failedAction(s)?.message}><Icon name="alert" size={11} /> {failedAction(s)?.label}</span
                >
              {:else if !u && fleetRefreshing}
                <span class="text-xs text-fjord-fg-faint">…</span>
              {:else if u?.state === 'available'}
                <span
                  class="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full text-fjord-warning bg-fjord-warning/10 border border-fjord-warning/30"
                  ><Icon name="arrow-up" size={11} /> Update</span
                >
              {:else if u?.state === 'upgrade'}
                <span
                  class="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full text-fjord-warning bg-fjord-warning/10 border border-fjord-warning/30"
                  ><Icon name="arrow-up" size={11} /> {u.fromVersion ? `v${u.fromVersion} → ` : ''}v{u.toVersion}</span
                >
              {:else if u?.state === 'pinned'}
                <span
                  class="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full text-fjord-accent bg-fjord-accent/10"
                  ><Icon name="pin" size={11} /> Pinned</span
                >
              {:else if u?.state === 'current'}
                <span class="inline-flex items-center gap-1 text-xs font-medium text-fjord-success"
                  ><Icon name="check" size={11} /> Up to date</span
                >
              {:else if u?.state === 'unknown'}
                <span class="inline-flex items-center gap-1 text-xs text-fjord-fg-dim cursor-help" title={u.detail || 'Could not compare the local image with the registry'}
                  ><Icon name="help" size={11} /> Unknown</span
                >
              {/if}
            </div>
            <!-- open link: the name once the app has one; the provider's own
                 row says Finish setup until it is ready -->
            <!-- Wide enough for a name like spike-web.apps.lan with its shield. -->
            <div class="w-52 shrink-0 flex items-center gap-1.5">
              {#if s.name === $names.provider && !$names.ready && $names.setupToken}
                <a
                  href={setupUrl($names, stacks)}
                  target="_blank"
                  rel="noreferrer"
                  on:click|stopPropagation
                  class="text-xs font-semibold px-2.5 py-1 rounded-lg bg-fjord-accent text-white hover:bg-fjord-accent-hover">Finish setup</a
                >
              {:else if info[s.name]?.link}
                {#if info[s.name]?.byName}<TrustMark {stacks} />{/if}
                <a
                  href={info[s.name].link}
                  target="_blank"
                  rel="noreferrer"
                  on:click|stopPropagation
                  class="text-xs font-mono text-fjord-accent hover:underline truncate flex items-center gap-1 {running(s.status)
                    ? ''
                    : 'opacity-40 pointer-events-none'}"
                  ><span class="truncate">{(info[s.name]?.link || '').replace(/^https?:\/\//, '')}</span>
                  <Icon name="external" size={11} /></a
                >
                {#if info[s.name]?.more?.length}
                  <span class="text-[11px] text-fjord-fg-muted" title={info[s.name].more?.join('\n')}
                    >+{(info[s.name].more?.length ?? 1) - 1} more apps</span
                  >
                {/if}
              {/if}
            </div>
            <!-- actions -->
            <div class="flex gap-1.5 shrink-0" on:click|stopPropagation>
              {#if running(s.status)}
                <button
                  on:click={() => act(s.name, 'down')}
                  disabled={!!s.busy}
                  title={s.busy ? busyWord(s.busy) : 'Stop'}
                  class="w-8 h-8 flex items-center justify-center rounded-lg bg-fjord-bg border border-fjord-border text-fjord-fg-muted hover:bg-fjord-border hover:text-fjord-fg disabled:opacity-40"
                  ><Icon name="stop" size={13} /></button
                >
              {:else}
                <button
                  on:click={() => act(s.name, 'up')}
                  disabled={!!s.busy}
                  title={s.busy ? busyWord(s.busy) : 'Start'}
                  class="w-8 h-8 flex items-center justify-center rounded-lg bg-fjord-bg border border-fjord-border text-fjord-fg-muted hover:bg-fjord-success/80 hover:text-white disabled:opacity-40"
                  ><Icon name="play" size={13} /></button
                >
              {/if}
              <button
                on:click={() => act(s.name, 'restart')}
                disabled={!!s.busy}
                title={s.busy ? busyWord(s.busy) : 'Restart'}
                class="w-8 h-8 flex items-center justify-center rounded-lg bg-fjord-bg border border-fjord-border text-fjord-fg-muted hover:bg-fjord-border hover:text-fjord-fg disabled:opacity-40"
                ><Icon name="restart" size={13} /></button
              >
              <button
                on:click={() => act(s.name, 'update')}
                disabled={!!s.busy}
                title={s.busy ? busyWord(s.busy) : 'Update'}
                class="w-8 h-8 flex items-center justify-center rounded-lg disabled:opacity-40 {u?.state === 'available'
                  ? 'bg-fjord-warning/10 border border-fjord-warning/40 text-fjord-warning'
                  : 'bg-fjord-bg border border-fjord-border text-fjord-fg-muted hover:bg-fjord-border hover:text-fjord-fg'}"
                ><Icon name="update" size={13} /></button
              >
            </div>
          </div>
        {/each}
      </div>
      {/if}
    {/if}
  </div>
</div>
