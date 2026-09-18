<script lang="ts">
  import { onMount, createEventDispatcher } from 'svelte';
  import Icon from './Icon.svelte';
  import EngineMark from './EngineMark.svelte';
  import EmptyState from './EmptyState.svelte';
  import { expandVars } from './expand';
  import { appUrl } from './appUrl';

  type ContainerStatus = {
    name: string;
    state: string;
    ports?: { hostPort: number; containerPort: number; protocol?: string }[];
    address?: string; // the container's own IP on an attachable network
  };
  type StackStatus = { state: string; containers: ContainerStatus[] };
  type Stack = { name: string; displayName?: string; icon?: string; compose?: string; env?: string; status?: StackStatus; state?: { origin?: { app_id?: string }; engine?: string } };
  export let stacks: Stack[] = [];
  // Fleet-wide update state, fetched once by the app shell (server-cached).
  export let fleet: Record<string, any> = {};
  export let fleetRefreshing = false;

  const dispatch = createEventDispatcher();

  import { appIcons, loadAppIcons, iconFor, tile } from './appIcons';
  // Resolve a stack's catalog icon: by its stored app_id first (stable), then by
  $: iconOf = (s: Stack) => iconFor($appIcons, s);
  let info: Record<string, { tag?: string; link?: string }> = {};

  const DOT: Record<string, string> = {
    running: 'bg-fjord-success',
    partial: 'bg-fjord-warning',
    stopped: 'bg-fjord-neutral',
    unknown: 'bg-fjord-danger',
  };
  const label = (s?: StackStatus) => s?.state ?? 'unknown';
  const running = (s?: StackStatus) => s?.state === 'running' || s?.state === 'partial';

  $: runningCount = stacks.filter((s) => running(s.status)).length;
  $: updateCount = stacks.filter(
    (s) => fleet[s.name]?.state === 'available' || fleet[s.name]?.state === 'upgrade'
  ).length;


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
    const next: Record<string, { tag?: string; link?: string }> = {};
    for (const s of stacks) {
      // Resolve ${VAR:-default} tags (e.g. immich's ${IMMICH_TAG:-latest})
      // before splitting, or the tag reads back as "-latest}".
      const img = expandVars(firstImage(s.compose || ''), envMap(s.env || ''));
      next[s.name] = { tag: img ? imgTag(img) : '', link: appUrl(s) };
    }
    info = next;
  }

  onMount(() => {
    loadAppIcons();
  });

  const act = (name: string, action: string) => dispatch('action', { name, action });
</script>

<div class="h-full flex flex-col">
  <div class="flex items-center justify-between mb-4 shrink-0">
    <div>
      <h2 class="text-2xl font-bold text-fjord-fg">Stacks</h2>
      <div class="text-sm text-fjord-fg-dim">
        {stacks.length} stack{stacks.length === 1 ? '' : 's'} · {runningCount} running{#if updateCount}
          · <span class="text-fjord-warning">{updateCount} update{updateCount === 1 ? '' : 's'} available</span>{/if}
      </div>
    </div>
    <div class="flex items-center gap-2">
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
    {#if stacks.length === 0}
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
      <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border bg-fjord-card">
        {#each stacks as s (s.name)}
          {@const u = fleet[s.name]}
          <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
          <div class="flex items-center gap-3.5 px-4 py-3 hover:bg-fjord-border/40 cursor-pointer" on:click={() => dispatch('select', s.name)}>
            <!-- icon, with the engine's mark overlaid -->
            <div class="relative shrink-0">
              {#if iconOf(s)}
                <img src={iconOf(s)} alt="" class="w-9 h-9 rounded-lg object-contain bg-fjord-bg p-0.5" />
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
            <div class="flex items-center gap-2 text-xs text-fjord-fg-muted w-24 shrink-0">
              <span class="w-2 h-2 rounded-full {DOT[label(s.status)]}"></span>
              <span class="capitalize">{label(s.status)}</span>
            </div>
            <!-- update badge -->
            <div class="w-32 shrink-0 text-center">
              {#if !u && fleetRefreshing}
                <span class="text-xs text-fjord-fg-faint">…</span>
              {:else if u?.state === 'available'}
                <span
                  class="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full text-fjord-warning bg-fjord-warning/10 border border-fjord-warning/30"
                  ><Icon name="arrow-up" size={11} /> Update</span
                >
              {:else if u?.state === 'upgrade'}
                <span
                  class="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full text-fjord-warning bg-fjord-warning/10 border border-fjord-warning/30"
                  ><Icon name="arrow-up" size={11} /> v{u.toVersion}</span
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
            <!-- open link -->
            <div class="w-40 shrink-0">
              {#if info[s.name]?.link}
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
              {/if}
            </div>
            <!-- actions -->
            <div class="flex gap-1.5 shrink-0" on:click|stopPropagation>
              {#if running(s.status)}
                <button
                  on:click={() => act(s.name, 'down')}
                  title="Stop"
                  class="w-8 h-8 flex items-center justify-center rounded-lg bg-fjord-bg border border-fjord-border text-fjord-fg-muted hover:bg-fjord-border hover:text-fjord-fg"
                  ><Icon name="stop" size={13} /></button
                >
              {:else}
                <button
                  on:click={() => act(s.name, 'up')}
                  title="Start"
                  class="w-8 h-8 flex items-center justify-center rounded-lg bg-fjord-bg border border-fjord-border text-fjord-fg-muted hover:bg-fjord-success/80 hover:text-white"
                  ><Icon name="play" size={13} /></button
                >
              {/if}
              <button
                on:click={() => act(s.name, 'restart')}
                title="Restart"
                class="w-8 h-8 flex items-center justify-center rounded-lg bg-fjord-bg border border-fjord-border text-fjord-fg-muted hover:bg-fjord-border hover:text-fjord-fg"
                ><Icon name="restart" size={13} /></button
              >
              <button
                on:click={() => act(s.name, 'update')}
                title="Update"
                class="w-8 h-8 flex items-center justify-center rounded-lg {u?.state === 'available'
                  ? 'bg-fjord-warning/10 border border-fjord-warning/40 text-fjord-warning'
                  : 'bg-fjord-bg border border-fjord-border text-fjord-fg-muted hover:bg-fjord-border hover:text-fjord-fg'}"
                ><Icon name="update" size={13} /></button
              >
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </div>
</div>
