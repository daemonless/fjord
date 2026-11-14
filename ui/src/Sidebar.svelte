<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { auth } from './auth';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import EngineMark from './EngineMark.svelte';
  import { fitMenu } from './fitMenu';
  import { appIcons, iconFor, tile, initial } from './appIcons';
  import {
    behind,
    computeFilterCounts,
    computeReorder,
    filterStacks,
    groupStacks,
    orderStacks,
    stackLabel as label,
    statusDot as dot,
    type GroupMode,
    type Stack,
    type StackFilter,
    type UpdateInfo,
  } from './stacks';

  export let stacks: Stack[] = [];
  export let stacksLoaded = false;
  export let stacksFailed: number | null = null;
  export let selectedStack: Stack | null = null;
  export let currentView: 'stacks' | 'store' | 'volumes' | 'networks' | 'system' | 'settings' | 'adopt' = 'stacks';
  export let fleet: Record<string, UpdateInfo> = {};
  export let updatingAll = false;
  export let daemonVersion = '';
  export let narrow = false;
  export let navOpen = false;
  export let onReload: () => Promise<void> = async () => {};

  const dispatch = createEventDispatcher<{
    select: Stack;
    navigate: {
      view: 'stacks' | 'store' | 'volumes' | 'networks' | 'system' | 'settings' | 'adopt';
      settingsTab?: 'storage' | 'extensions' | 'catalogs' | 'advanced' | 'account';
    };
    new: void;
    'update-all': void;
  }>();

  let search = ''; // sidebar stack filter
  // The sidebar's + menu: the store, Adopt, or a blank stack.
  let newOpen = false;
  function addBy(how: 'store' | 'adopt' | 'new') {
    newOpen = false;
    if (how === 'new') {
      dispatch('new');
      return;
    }
    dispatch('navigate', { view: how });
  }

  // Resizable panels, persisted across refreshes. Mutations go through named
  // functions (not inline arrows in the action param) so Svelte 5 instruments
  // the assignment reactively AND the localStorage save actually runs.
  const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
  const loadSize = (key: string, def: number) => {
    try {
      return Number(localStorage.getItem(key)) || def;
    } catch {
      return def;
    }
  };
  const saveSize = (key: string, v: number) => {
    try {
      localStorage.setItem(key, String(v));
    } catch {}
  };
  let sidebarWidth = loadSize('fjord.sidebarW', 288); // w-72
  function setSidebar(w: number) {
    sidebarWidth = clamp(w, 200, 640);
    saveSize('fjord.sidebarW', sidebarWidth);
  }

  // Drag-to-resize action: reports incremental pointer deltas to onMove.
  function resizer(node: HTMLElement, opts: { axis: 'x' | 'y'; onMove: (delta: number) => void }) {
    let last = 0;
    const pos = (e: PointerEvent) => (opts.axis === 'x' ? e.clientX : e.clientY);
    function move(e: PointerEvent) {
      const cur = pos(e);
      opts.onMove(cur - last);
      last = cur;
    }
    function up(e: PointerEvent) {
      node.releasePointerCapture(e.pointerId);
      node.removeEventListener('pointermove', move);
      node.removeEventListener('pointerup', up);
      document.body.style.cursor = '';
      document.body.style.userSelect = '';
    }
    function down(e: PointerEvent) {
      e.preventDefault();
      last = pos(e);
      node.setPointerCapture(e.pointerId);
      node.addEventListener('pointermove', move);
      node.addEventListener('pointerup', up);
      document.body.style.cursor = opts.axis === 'x' ? 'col-resize' : 'row-resize';
      document.body.style.userSelect = 'none';
    }
    node.addEventListener('pointerdown', down);
    return { destroy: () => node.removeEventListener('pointerdown', down) };
  }

  // The status filter under the search box. Remembered; a chip shows only
  // when it has something in it, so a healthy host shows next to nothing.
  let stackFilter: StackFilter = (() => {
    try {
      const v = localStorage.getItem('fjord.stackFilter') as StackFilter;
      return ['all', 'running', 'stopped', 'problem', 'updates'].includes(v) ? v : 'all';
    } catch {
      return 'all';
    }
  })();
  $: try {
    localStorage.setItem('fjord.stackFilter', stackFilter);
  } catch {}
  $: filterCounts = computeFilterCounts(stacks, fleet);
  // A filter emptied by events (the last problem fixed) falls back to All
  // rather than leaving an empty list with no chip to explain it.
  $: if (stackFilter !== 'all' && !filterCounts[stackFilter]) stackFilter = 'all';
  $: filtered = filterStacks(stacks, search, stackFilter, fleet);

  // Sidebar grouping by state.group. Ungrouped ('') sorts last, no header.
  let collapsedGroups: Record<string, boolean> = {};
  try {
    collapsedGroups = JSON.parse(localStorage.getItem('fjord.collapsedGroups') || '{}');
  } catch {}
  function toggleGroup(g: string) {
    collapsedGroups[g] = !collapsedGroups[g];
    collapsedGroups = collapsedGroups;
    try {
      localStorage.setItem('fjord.collapsedGroups', JSON.stringify(collapsedGroups));
    } catch {}
  }
  $: ordered = orderStacks(filtered);
  // Sidebar grouping mode: user-defined groups, by engine, network, status,
  // or flat.
  // Only 'groups' is drag-editable (the others are derived buckets).
  let groupMode: GroupMode = 'groups';
  let groupMenuOpen = false;
  try {
    const m = localStorage.getItem('fjord.groupMode') as GroupMode;
    if (['groups', 'engine', 'app', 'network', 'status', 'flat'].includes(m)) groupMode = m;
  } catch {}
  function setGroupMode(m: GroupMode) {
    groupMode = m;
    try {
      localStorage.setItem('fjord.groupMode', m);
    } catch {}
  }
  const GROUP_MODES: { id: GroupMode; label: string }[] = [
    { id: 'groups', label: 'Groups' },
    { id: 'engine', label: 'Engine' },
    { id: 'app', label: 'App' },
    { id: 'network', label: 'Network' },
    { id: 'status', label: 'Status' },
    { id: 'flat', label: 'Flat' },
  ];
  $: groupedStacks = groupStacks(ordered, groupMode);
  // Headers show for user groups (when present) and always for engine/status
  // buckets; flat has no headers. Drag-to-regroup is groups-only.
  $: hasGroups = groupMode === 'groups' && groupedStacks.some(([g]) => g !== '');
  $: draggableStacks = groupMode === 'groups';

  // ---- sidebar drag-and-drop: reorder stacks and move them between groups ----
  let dragName = ''; // stack being dragged
  let dropTarget = ''; // row currently hovered ('' = none)
  let dropAfter = false; // insert below (vs above) the hovered row
  let dropGroup: string | null = null; // group header/zone hovered ('' = ungrouped, null = none)
  const groupOf = (name: string) => (stacks.find((s) => s.name === name)?.state?.group || '').trim();

  function onRowDragOver(e: DragEvent, name: string) {
    if (!dragName) return;
    e.preventDefault();
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    dropAfter = e.clientY > r.top + r.height / 2;
    dropTarget = name;
    dropGroup = null;
  }
  function onGroupDragOver(e: DragEvent, group: string) {
    if (!dragName) return;
    e.preventDefault();
    dropGroup = group;
    dropTarget = '';
  }
  function clearDrag() {
    dragName = '';
    dropTarget = '';
    dropGroup = null;
  }

  async function commitReorder(targetName: string | null, after: boolean, group: string) {
    const res = computeReorder(ordered, stacks, dragName, targetName, after, group);
    clearDrag();
    if (!res) return;
    stacks = res.nextStacks;
    try {
      await fetch('/api/stacks/reorder', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ items: res.items }),
      });
    } catch {}
    await onReload(); // reconcile with server truth
  }

  // Count only stacks that still exist: the cache can outlive a deleted one.
  $: fleetBehindCount = stacks.filter((s) => behind(fleet[s.name])).length;
  $: fleetAvailableCount = stacks.filter((s) => behind(fleet[s.name]) && !s.busy).length;

  async function signOut() {
    await fetch('/api/auth/logout', { method: 'POST' }).catch(() => {});
    location.reload();
  }
</script>

<svelte:window
  on:click={(e) => {
    const t = e.target as HTMLElement;
    if (newOpen && !t?.closest?.('.new-menu')) newOpen = false;
  }}
/>

{#if narrow && navOpen}
  <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
  <div class="fixed inset-0 z-30 bg-black/50" on:click={() => (navOpen = false)}></div>
{/if}
<!-- Sidebar: stack list (Dockge-style); a drawer on phones -->
<aside
  class="shrink-0 bg-fjord-bg flex flex-col {narrow ? (navOpen ? 'fixed inset-y-0 left-0 z-40 w-[85vw] max-w-xs shadow-2xl border-r border-fjord-border' : 'hidden') : ''}"
  style={narrow ? '' : `width:${sidebarWidth}px`}>
  <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
  <div
    class="flex items-center gap-2.5 text-xl font-bold tracking-widest text-fjord-fg cursor-pointer px-5 py-4 border-b border-fjord-border"
    on:click={() => dispatch('navigate', { view: 'stacks' })}
  >
    <Icon name="mountain" size={20} class="text-fjord-accent" />
    <span><span class="text-fjord-accent">F</span>JORD</span>
  </div>

  <div class="p-3 flex gap-2 border-b border-fjord-border">
    <input
      id="stack-search"
      bind:value={search}
      placeholder="Search stacks…"
      class="flex-1 min-w-0 bg-fjord-card border border-fjord-border rounded-md px-3 py-1.5 text-sm text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
    />
    <!-- Every way to add a stack, so Adopt is found where people look. -->
    <details class="new-menu relative shrink-0" bind:open={newOpen}>
      <summary
        title="Add a stack"
        aria-label="Add a stack"
        class="list-none cursor-pointer h-full w-9 flex items-center justify-center bg-fjord-accent hover:bg-fjord-accent-hover text-white rounded-md"
        ><Icon name="plus" /></summary
      >
      <div use:fitMenu class="absolute z-30 left-0 mt-1 w-72 bg-fjord-card border border-fjord-border rounded-lg shadow-xl py-1">
        <button on:click={() => addBy('store')} class="w-full text-left px-3 py-2 hover:bg-fjord-border/50"
          ><span class="block text-sm font-medium text-fjord-fg-body">Install from the store</span><span class="block text-xs text-fjord-fg-muted"
            >Pick an app; fjord fills in the rest</span
          ></button
        >
        <button on:click={() => addBy('adopt')} class="w-full text-left px-3 py-2 hover:bg-fjord-border/50"
          ><span class="block text-sm font-medium text-fjord-fg-body">Adopt existing containers</span><span class="block text-xs text-fjord-fg-muted"
            >Take over ones started outside fjord</span
          ></button
        >
        <button on:click={() => addBy('new')} class="w-full text-left px-3 py-2 hover:bg-fjord-border/50"
          ><span class="block text-sm font-medium text-fjord-fg-body">New stack</span><span class="block text-xs text-fjord-fg-muted"
            >Write the compose file yourself</span
          ></button
        >
      </div>
    </details>
  </div>

  <!-- status filter: only the chips with something in them -->
  {#if filterCounts.problem || filterCounts.updates || (filterCounts.running && filterCounts.stopped)}
    <div class="flex flex-wrap gap-1 px-3 pt-2">
      {#each [['all', 'All', stacks.length], ['running', 'Running', filterCounts.running], ['stopped', 'Stopped', filterCounts.stopped], ['problem', 'Problems', filterCounts.problem], ['updates', 'Updates', filterCounts.updates]] as [id, text, n]}
        {#if id === 'all' || n}
          <button
            on:click={() => (stackFilter = stackFilter === id && id !== 'all' ? 'all' : (id as StackFilter))}
            aria-pressed={stackFilter === id}
            class="px-2 py-0.5 rounded-full text-[11px] font-medium border transition-colors {stackFilter === id
              ? 'bg-fjord-accent text-white border-fjord-accent'
              : id === 'problem'
                ? 'text-fjord-danger border-fjord-danger/40 hover:bg-fjord-danger/10'
                : id === 'updates'
                  ? 'text-fjord-warning border-fjord-warning/40 hover:bg-fjord-warning/10'
                  : 'text-fjord-fg-dim border-fjord-border hover:text-fjord-fg-secondary'}">{text} {n}</button
          >
        {/if}
      {/each}
    </div>
  {/if}

  <!-- group-by selector: how the stack list is bucketed -->
  <div class="relative px-3 pb-1 pt-0.5">
    <button
      on:click={() => (groupMenuOpen = !groupMenuOpen)}
      class="flex items-center gap-1.5 text-[11px] font-medium text-fjord-fg-dim hover:text-fjord-fg-secondary transition-colors"
    >
      <span class="text-[10px] uppercase tracking-wide text-fjord-fg-faint">Group by</span>
      <span class="text-fjord-fg-secondary">{GROUP_MODES.find((g) => g.id === groupMode)?.label}</span>
      <Icon name={groupMenuOpen ? 'chevron-up' : 'chevron-down'} size={11} />
    </button>
    {#if groupMenuOpen}
      <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
      <div class="fixed inset-0 z-20" on:click={() => (groupMenuOpen = false)}></div>
      <div class="absolute z-30 left-3 top-full mt-1 bg-fjord-card border border-fjord-border rounded-lg shadow-2xl p-1 min-w-36">
        {#each GROUP_MODES as gm}
          <button
            on:click={() => { setGroupMode(gm.id); groupMenuOpen = false; }}
            class="w-full flex items-center justify-between gap-3 px-3 py-1.5 rounded-md text-sm transition-colors {groupMode ===
            gm.id
              ? 'bg-fjord-border text-fjord-fg'
              : 'text-fjord-fg-secondary hover:bg-fjord-border/50'}"
          >
            {gm.label}
            {#if groupMode === gm.id}<Icon name="check" size={13} class="text-fjord-accent" />{/if}
          </button>
        {/each}
      </div>
    {/if}
  </div>

  <div class="flex-1 overflow-y-auto py-2">
    {#each groupedStacks as [group, items] (group)}
      {#if group || hasGroups}
        <!-- svelte-ignore a11y-no-static-element-interactions -->
        <button
          on:click={() => group && toggleGroup(group)}
          on:dragover={(e) => onGroupDragOver(e, group)}
          on:drop={() => commitReorder(null, false, group)}
          class="w-full flex items-center gap-1.5 px-3 pt-3 pb-1 text-[11px] font-semibold uppercase tracking-wide rounded {group
            ? 'text-fjord-fg-dim hover:text-fjord-fg-secondary'
            : 'text-fjord-fg-faint'} {dropGroup === group ? '!bg-fjord-accent/20 ring-1 ring-fjord-accent/50 !text-fjord-fg-body' : ''}"
        >
          {#if group}
            <Icon name={collapsedGroups[group] ? 'chevron-right' : 'chevron-down'} size={11} />
          {/if}
          {#if groupMode === 'engine' && group}
            <EngineMark engine={group} size={13} strokeWidth={2.25} />
          {/if}
          <span class="truncate">{group || 'Ungrouped'}</span>
          <span class="text-fjord-fg-faint normal-case">{items.length}</span>
        </button>
      {/if}
      {#if !collapsedGroups[group]}
        <div class={group ? 'ml-3.5 pl-1 border-l border-fjord-border' : ''}>
          {#if items.length === 0 && groupMode === 'groups'}
            <!-- svelte-ignore a11y-no-static-element-interactions -->
            <div
              on:dragover={(e) => onGroupDragOver(e, group)}
              on:drop={() => commitReorder(null, false, group)}
              class="mx-3 my-1 px-3 py-3 text-[11px] text-center rounded border border-dashed transition-colors {dropGroup ===
              group
                ? 'border-fjord-accent text-fjord-fg-body bg-fjord-accent/10'
                : 'border-fjord-border text-fjord-fg-faint'}"
            >
              Drag here to remove from group
            </div>
          {/if}
          {#each items as stack (stack.name)}
            <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
            <div
              draggable={draggableStacks}
              on:dragstart={() => (dragName = stack.name)}
              on:dragend={clearDrag}
              on:dragover={(e) => onRowDragOver(e, stack.name)}
              on:drop={() => commitReorder(stack.name, dropAfter, groupOf(stack.name))}
              on:click={() => dispatch('select', stack)}
              class="flex items-center gap-3 py-2 pl-4 pr-4 {draggableStacks
                ? 'cursor-grab active:cursor-grabbing'
                : 'cursor-pointer'} text-sm transition-colors border-y-2 border-transparent {selectedStack?.name ===
                stack.name && currentView === 'stacks'
                ? 'bg-fjord-border text-fjord-fg'
                : 'text-fjord-fg-secondary hover:bg-fjord-border/40'} {dragName === stack.name
                ? 'opacity-40'
                : ''} {dropTarget === stack.name && !dropAfter
                ? '!border-t-fjord-accent'
                : ''} {dropTarget === stack.name && dropAfter ? '!border-b-fjord-accent' : ''}"
            >
              <span class="w-2 h-2 rounded-full shrink-0 {dot(stack.status)}"></span>
              {#if iconFor($appIcons, stack)}
                <img src={iconFor($appIcons, stack)} alt="" draggable="false" class="w-4 h-4 rounded shrink-0 object-contain" />
              {:else}
                <span class="w-4 h-4 rounded shrink-0 flex items-center justify-center text-[9px] font-bold text-fjord-fg" style="background:{tile(label(stack))}">{initial(label(stack))}</span>
              {/if}
              <span class="truncate">{label(stack)}</span>
              {#if stack.busy === 'update'}
                <span class="ml-auto shrink-0 text-fjord-accent" title="Updating…"><Spinner size={11} /></span>
              {:else if behind(fleet[stack.name])}
                <span
                  class="ml-auto shrink-0 text-fjord-warning"
                  title={fleet[stack.name].state === 'upgrade'
                    ? `${fleet[stack.name].fromVersion ? `v${fleet[stack.name].fromVersion} → ` : ''}v${fleet[stack.name].toVersion} available`
                    : 'Update available'}><Icon name="arrow-up" size={12} /></span
                >
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    {/each}
    {#if fleetBehindCount > 0}
      <div class="px-4 pt-3 flex items-center justify-between gap-2 text-[11px] text-fjord-warning/80">
        <span class="flex items-center gap-1.5">
          <Icon name="arrow-up" size={11} />
          {fleetBehindCount} update{fleetBehindCount === 1 ? '' : 's'} available
        </span>
        {#if fleetAvailableCount > 0}
          <button
            on:click={() => dispatch('update-all')}
            disabled={updatingAll}
            class="font-medium text-fjord-warning hover:underline disabled:opacity-50"
            >Update all</button
          >
        {/if}
      </div>
    {/if}
    {#if !stacksLoaded}
      <p class="px-4 py-2 text-xs text-fjord-fg-faint italic">{stacksFailed !== null ? 'Stacks did not load.' : 'Loading…'}</p>
    {:else if filtered.length === 0}
      <p class="px-4 py-2 text-xs text-fjord-fg-faint italic">No stacks{search || stackFilter !== 'all' ? ' match' : ' yet'}.</p>
    {/if}
  </div>

  <nav class="border-t border-fjord-border p-2 flex flex-col gap-1">
    <button
      on:click={() => dispatch('navigate', { view: 'store' })}
      class="flex items-center gap-2.5 text-left px-3 py-2 rounded-md text-sm font-medium transition-colors {currentView ===
      'store'
        ? 'bg-fjord-border text-fjord-fg'
        : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="store" size={15} /> App Store</button
    >
    <button
      on:click={() => dispatch('navigate', { view: 'volumes' })}
      class="flex items-center gap-2.5 text-left px-3 py-2 rounded-md text-sm font-medium transition-colors {currentView ===
      'volumes'
        ? 'bg-fjord-border text-fjord-fg'
        : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="drive" size={15} /> Volumes</button
    >
    <button
      on:click={() => dispatch('navigate', { view: 'networks' })}
      class="flex items-center gap-2.5 text-left px-3 py-2 rounded-md text-sm font-medium transition-colors {currentView ===
      'networks'
        ? 'bg-fjord-border text-fjord-fg'
        : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="globe" size={15} /> Networks</button
    >
    <button
      on:click={() => dispatch('navigate', { view: 'system' })}
      class="flex items-center gap-2.5 text-left px-3 py-2 rounded-md text-sm font-medium transition-colors {currentView ===
      'system'
        ? 'bg-fjord-border text-fjord-fg'
        : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="activity" size={15} /> System</button
    >
    <button
      on:click={() => dispatch('navigate', { view: 'settings' })}
      class="flex items-center gap-2.5 text-left px-3 py-2 rounded-md text-sm font-medium transition-colors {currentView ===
      'settings'
        ? 'bg-fjord-border text-fjord-fg'
        : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="settings" size={15} /> Settings</button
    >
  </nav>
  {#if $auth && $auth !== 'unreachable' && $auth.off}
    <button
      on:click={() => dispatch('navigate', { view: 'settings', settingsTab: 'account' })}
      title="FJORD_AUTH=none: only this host and the proxy in front of it can reach fjord"
      class="mx-3 mb-2 px-2 py-1.5 rounded-md text-left text-xs text-fjord-warning border border-fjord-warning/40">Sign-in is off</button
    >
  {:else if $auth && $auth !== 'unreachable' && $auth.user}
    <div class="flex items-center gap-2 px-4 pb-1 text-xs text-fjord-fg-dim">
      <span class="min-w-0 truncate" title="Signed in as {$auth.user}">{$auth.user}</span>
      <button on:click={signOut} class="ml-auto shrink-0 hover:text-fjord-fg">Sign out</button>
    </div>
  {/if}
  <!-- Which fjord this is, where anyone reporting a problem can see it. -->
  {#if daemonVersion}
    <button
      on:click={() => dispatch('navigate', { view: 'system' })}
      title="System: this host and fjord"
      class="px-4 pb-3 text-left text-[11px] text-fjord-fg-faint hover:text-fjord-fg-dim">fjord {daemonVersion}</button
    >
  {/if}
</aside>

<!-- sidebar↔main sash: visible divider, widens + highlights on hover -->
{#if !narrow}
  <div
    role="slider"
    aria-orientation="vertical"
    aria-label="Sidebar width"
    aria-valuenow={sidebarWidth}
    aria-valuemin={200}
    aria-valuemax={640}
    tabindex="0"
    class="group relative w-px shrink-0 bg-fjord-border cursor-col-resize focus:outline-none focus:bg-fjord-accent"
    use:resizer={{ axis: 'x', onMove: (d) => setSidebar(sidebarWidth + d) }}
    on:dblclick={() => setSidebar(288)}
    on:keydown={(e) => { if (e.key === 'ArrowLeft') setSidebar(sidebarWidth - 16); else if (e.key === 'ArrowRight') setSidebar(sidebarWidth + 16); else if (e.key === 'Enter') setSidebar(288); }}
    title="Drag to resize · double-click to reset"
  >
    <div class="absolute inset-y-0 -left-1 -right-1 group-hover:bg-fjord-accent/60 transition-colors"></div>
  </div>
{/if}
