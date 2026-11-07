<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import Terminal from './Terminal.svelte';
  import LogView from './LogView.svelte';
  import Shell from './Shell.svelte';
  import Icon from './Icon.svelte';
  import { primaryContainer, serviceLabel, type Stack } from './stacks';

  export let stack: Stack;
  export let open = true;
  export let tab: 'output' | 'logs' | 'shell' = 'output';
  export let outputLogs = '';
  export let termStatus: 'idle' | 'running' | 'error' = 'idle';
  export let statusMessage = '';

  let containerLogs: Record<string, string> = {};

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

  let drawerHeight = loadSize('fjord.drawerH', 256); // h-64
  function setDrawer(h: number) {
    drawerHeight = clamp(h, 120, 640);
    saveSize('fjord.drawerH', drawerHeight);
  }

  function setDrawerOpen(v: boolean) {
    open = v;
    try {
      localStorage.setItem('fjord.drawerOpen', v ? '1' : '0');
    } catch {}
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

  // The container a shell exec's into: the stack's first (primary) container.
  // Shell target container: the user's pick if still valid, else the stack's
  // first container. Falls back automatically when switching stacks.
  let shellPick = '';
  $: shellContainers = stack.status?.containers ?? [];
  $: shellContainer =
    shellPick && shellContainers.some((c) => c.name === shellPick)
      ? shellPick
      : primaryContainer(stack.compose ?? '', shellContainers);
  // A shell needs a live container/jail: exec into a stopped one fails
  // instantly ("Cannot find the jail") and would just reconnect in a loop.
  $: shellContainerUp = (shellContainers.find((c) => c.name === shellContainer)?.state ?? 'stopped') !== 'stopped';

  // Live log streaming. `podman compose logs -f` never exits, so it's driven by
  // an AbortController rather than the action flow -- aborting (close drawer /
  // switch stacks / re-click) disconnects, which kills the podman process.
  let logController: AbortController | null = null;
  let logStacking = ''; // which stack we're tailing, '' when idle

  // Logs scope: per-service checkboxes, all checked by default (a missing key
  // counts as checked, so new containers join automatically).
  let logsSel: Record<string, boolean> = {};
  let logsMenuOpen = false;
  $: logsSelected = shellContainers.filter((c) => logsSel[c.name] !== false).map((c) => c.name);
  $: logsAll = shellContainers.length > 0 && logsSelected.length === shellContainers.length;
  // All, None, or one service alone: following one of ten (crafting-apps)
  // was nine unticks.
  function setLogs(pick: (name: string) => boolean) {
    for (const c of shellContainers) logsSel[c.name] = pick(c.name);
    logsSel = logsSel;
    streamLogs(stack.name);
  }

  export async function streamLogs(name: string) {
    logController?.abort();
    const ctrl = new AbortController();
    logController = ctrl;
    logStacking = name;
    containerLogs[name] = ''; // backend announces its own "$ command" header
    open = true;
    // Compute the scope HERE, not from the $: derived vars -- checkbox
    // handlers call this before Svelte reruns reactives, so those are stale.
    const selected = shellContainers.filter((c) => logsSel[c.name] !== false).map((c) => c.name);
    const all = selected.length === shellContainers.length;
    if (shellContainers.length > 1 && selected.length === 0) {
      containerLogs[name] = '[no services selected: pick one under Services, or All]\n';
      if (logController === ctrl) logStacking = '';
      return;
    }
    try {
      const scope = all ? '' : selected.map((c) => `&container=${encodeURIComponent(c)}`).join('');
      const res = await fetch(`/api/stacks/${name}/logs?follow=1&tail=200${scope}`, { signal: ctrl.signal });
      if (!res.ok) {
        // The daemon says why; the status alone told nobody anything.
        containerLogs[name] += `[ERROR]: ${(await res.text()).trim() || `HTTP ${res.status}`}\n`;
        return;
      }
      const reader = res.body?.getReader();
      if (reader) {
        const decoder = new TextDecoder('utf-8');
        while (true) {
          const { done, value } = await reader.read();
          if (done) {
            const tail = decoder.decode();
            if (tail) containerLogs[name] += tail;
            break;
          }
          containerLogs[name] += decoder.decode(value, { stream: true });
        }
      }
    } catch (err: any) {
      if (err?.name !== 'AbortError') containerLogs[name] += `[ERROR]: ${err?.message || err}\n`;
    } finally {
      if (logController === ctrl && logStacking === name) logStacking = '';
    }
  }

  export function stopLogs() {
    logController?.abort();
    logController = null;
    logStacking = '';
  }

  export function resetLogs() {
    stopLogs(); // don't keep tailing a stack we're navigating away from
    logsSel = {}; // logs scope is per-stack
    logsMenuOpen = false;
  }

  export function followLogsIfActive(name: string) {
    if (open && tab === 'logs' && logStacking !== name) streamLogs(name);
  }

  // Switch the bottom panel's tab. Selecting Logs starts the live stream for
  // this stack; leaving it stops the follow (killed server-side on disconnect).
  function showTab(name: string, nextTab: 'output' | 'logs' | 'shell') {
    tab = nextTab;
    open = true;
    try {
      localStorage.setItem('fjord.drawerTab', nextTab);
    } catch {}
    if (nextTab === 'logs') {
      if (logStacking !== name) streamLogs(name);
    } else {
      stopLogs(); // shell/terminal don't use the logs stream
    }
  }

  onMount(() => {
    // The Logs tab persists across refreshes -- if it's the active view when
    // the drawer first mounts for a stack, start streaming now.
    if (open && tab === 'logs') streamLogs(stack.name);
  });

  onDestroy(() => {
    stopLogs();
  });
</script>

<!-- Terminal drawer -->
{#if open}
  <!-- editor↔drawer sash: drag up = taller, widens + highlights on hover -->
  <div
    role="slider"
    aria-orientation="horizontal"
    aria-label="Output height"
    aria-valuenow={drawerHeight}
    aria-valuemin={120}
    aria-valuemax={640}
    tabindex="0"
    class="group relative h-px shrink-0 my-2 bg-fjord-border cursor-row-resize focus:outline-none focus:bg-fjord-accent"
    use:resizer={{ axis: 'y', onMove: (d) => setDrawer(drawerHeight - d) }}
    on:dblclick={() => setDrawer(256)}
    on:keydown={(e) => { if (e.key === 'ArrowUp') setDrawer(drawerHeight + 16); else if (e.key === 'ArrowDown') setDrawer(drawerHeight - 16); else if (e.key === 'Enter') setDrawer(256); }}
    title="Drag to resize · double-click to reset"
  >
    <div class="absolute inset-x-0 -top-1.5 -bottom-1.5 group-hover:bg-fjord-accent/60 transition-colors"></div>
  </div>
  <div class="shrink-0 flex flex-col min-h-0" style="height:{drawerHeight}px">
    <!-- tab bar -->
    <div class="flex items-center gap-1 mb-1">
      <button
        on:click={() => showTab(stack.name, 'output')}
        class="flex items-center gap-1.5 px-3 py-1 rounded-t-md text-xs font-medium transition-colors {tab ===
        'output'
          ? 'bg-fjord-card text-fjord-fg border-b-2 border-fjord-accent'
          : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="list" size={13} /> Output</button
      >
      <button
        on:click={() => showTab(stack.name, 'logs')}
        class="px-3 py-1 rounded-t-md text-xs font-medium transition-colors flex items-center gap-1.5 {tab ===
        'logs'
          ? 'bg-fjord-card text-fjord-fg border-b-2 border-fjord-accent'
          : 'text-fjord-fg-muted hover:text-fjord-fg'}"
      >
        <Icon name="logs" size={13} /> Logs
        {#if logStacking === stack.name}
          <span class="w-1.5 h-1.5 rounded-full bg-fjord-warning animate-pulse"></span>
        {/if}
      </button>
      {#if tab === 'logs' && shellContainers.length > 1}
        <div class="relative ml-1">
          <button
            on:click={() => (logsMenuOpen = !logsMenuOpen)}
            title="Choose which services' logs to show"
            class="bg-fjord-bg border border-fjord-border rounded px-2 py-0.5 text-[11px] font-mono focus:outline-none {logsAll
              ? 'text-fjord-fg-secondary'
              : 'text-fjord-accent border-fjord-accent/50'}"
            ><span class="flex items-center gap-1"
              >{logsAll ? 'Services' : `${logsSelected.length}/${shellContainers.length} services`}
              <Icon name="chevron-down" size={11} /></span
            ></button
          >
          {#if logsMenuOpen}
            <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
            <div class="fixed inset-0 z-20" on:click={() => (logsMenuOpen = false)}></div>
            <div class="absolute z-30 top-full left-0 mt-1 bg-fjord-card border border-fjord-border rounded-lg shadow-2xl p-2 min-w-48">
              <div class="flex items-center gap-3 px-1.5 pb-1.5 mb-1 border-b border-fjord-border text-xs">
                <button on:click={() => setLogs(() => true)} disabled={logsAll} class="text-fjord-accent hover:underline disabled:text-fjord-fg-faint disabled:no-underline">All</button>
                <button on:click={() => setLogs(() => false)} disabled={logsSelected.length === 0} class="text-fjord-accent hover:underline disabled:text-fjord-fg-faint disabled:no-underline">None</button>
              </div>
              {#each shellContainers as c}
                <label class="group flex items-center gap-2 px-1.5 py-1 rounded text-xs text-fjord-fg-secondary cursor-pointer hover:bg-fjord-border/50 hover:text-fjord-fg select-none">
                  <input
                    type="checkbox"
                    checked={logsSel[c.name] !== false}
                    on:change={(e) => {
                      logsSel[c.name] = e.currentTarget.checked;
                      logsSel = logsSel;
                      streamLogs(stack.name); // restart scoped
                    }}
                    class="accent-fjord-accent"
                  />
                  <span class="font-mono flex-1">{serviceLabel(stack.name, c.name)}</span>
                  <button
                    on:click|preventDefault|stopPropagation={() => setLogs((n) => n === c.name)}
                    title="Show only {serviceLabel(stack.name, c.name)}"
                    class="opacity-0 group-hover:opacity-100 focus:opacity-100 text-[11px] text-fjord-accent hover:underline">only</button
                  >
                </label>
              {/each}
            </div>
          {/if}
        </div>
      {/if}
      <button
        on:click={() => showTab(stack.name, 'shell')}
        class="flex items-center gap-1.5 px-3 py-1 rounded-t-md text-xs font-medium transition-colors {tab ===
        'shell'
          ? 'bg-fjord-card text-fjord-fg border-b-2 border-fjord-accent'
          : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="terminal" size={13} /> Shell</button
      >
      {#if tab === 'shell' && shellContainers.length > 1}
        <select
          value={shellContainer}
          on:change={(e) => (shellPick = e.currentTarget.value)}
          title="Service to open a shell in"
          class="ml-1 bg-fjord-bg border border-fjord-border rounded px-2 py-0.5 text-[11px] text-fjord-fg-secondary font-mono focus:outline-none focus:border-fjord-accent"
        >
          {#each shellContainers as c}
            <option value={c.name}>{serviceLabel(stack.name, c.name)}</option>
          {/each}
        </select>
      {/if}
      <div class="flex-1"></div>
      <button
        on:click={() => {
          stopLogs();
          setDrawerOpen(false);
        }}
        class="flex items-center gap-1 text-xs font-medium text-fjord-fg-muted hover:text-fjord-fg"
        ><Icon name="chevron-down" size={12} /> Hide</button
      >
    </div>
    {#if tab === 'logs'}
      <LogView
        text={containerLogs[stack.name] || ''}
        streaming={logStacking === stack.name}
      />
    {:else if tab === 'shell'}
      {#if shellContainer && shellContainerUp}
        {#key stack.name + '/' + shellContainer}
          <Shell stack={stack.name} container={shellContainer} />
        {/key}
      {:else}
        <div
          class="flex-1 flex items-center justify-center text-sm text-fjord-fg-dim bg-fjord-card border border-fjord-border rounded-xl"
        >
          Start the stack to open a shell.
        </div>
      {/if}
    {:else}
      {#key stack.name}
        <Terminal
          bind:logs={outputLogs}
          status={termStatus}
          {statusMessage}
        />
      {/key}
    {/if}
  </div>
{:else}
  <button
    on:click={() => setDrawerOpen(true)}
    class="shrink-0 mt-3 flex items-center gap-2 px-4 py-2 border border-fjord-border rounded-lg text-xs font-medium text-fjord-fg-muted hover:text-fjord-fg hover:border-fjord-accent/40 transition-colors"
  >
    <Icon name="chevron-up" size={12} /> Panel
    {#if termStatus === 'running'}
      <span class="w-1.5 h-1.5 rounded-full bg-fjord-warning animate-ping"></span>
    {:else if outputLogs}
      <span class="text-fjord-fg-dim">· last output</span>
    {/if}
  </button>
{/if}
