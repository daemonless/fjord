<script lang="ts">
  import { isoDateTime } from './when';
  // One line per thing that happened: to one stack (stack set) or to all of
  // them, deleted ones included (stack unset).
  import { onMount } from 'svelte';
  import { changeLines, historyTitle, whenLabel, type HistoryEntry } from './history';

  export let stack = '';
  export let limit = 100;
  /** Bumped by the parent to read again, e.g. after an action ends. */
  export let reload = 0;

  let entries: HistoryEntry[] = [];
  let loading = true;
  let failedToLoad = false;

  async function load(_stack: string, _reload: number) {
    const url = stack ? `/api/stacks/${encodeURIComponent(stack)}/history?limit=${limit}` : `/api/history?limit=${limit}`;
    try {
      const r = await fetch(url);
      if (!r.ok) throw new Error(String(r.status));
      entries = await r.json();
      failedToLoad = false;
    } catch {
      failedToLoad = true;
    }
    loading = false;
  }

  // Again whenever the stack or reload changes, and every half minute for
  // what happened elsewhere.
  $: load(stack, reload);
  onMount(() => {
    const t = setInterval(() => load(stack, reload), 30000);
    return () => clearInterval(t);
  });
</script>

{#if loading}
  <p class="text-sm text-fjord-fg-muted">Loading…</p>
{:else if failedToLoad}
  <p class="text-sm text-fjord-fg-muted">History could not be read.</p>
{:else if entries.length === 0}
  <p class="text-sm text-fjord-fg-muted" data-history-empty>Nothing has happened here yet.</p>
{:else}
  <ul class="divide-y divide-fjord-border text-sm" data-history>
    {#each entries as e (e.at + e.stack + e.action)}
      <li class="py-2 flex gap-4" data-history-entry data-action={e.action} data-result={e.result}>
        <span class="w-32 shrink-0 text-fjord-fg-muted tabular-nums" title={isoDateTime(e.at, true)}>{whenLabel(e.at)}</span>
        <div class="min-w-0">
          <div class={e.result === 'failed' ? 'text-fjord-danger' : 'text-fjord-fg'}>
            {#if !stack}<span class="font-semibold">{e.stack}</span>{': '}{/if}{historyTitle(e)}
          </div>
          {#each changeLines(e) as line}
            <div class="text-xs text-fjord-fg-muted font-mono">{line}</div>
          {/each}
          {#if e.message}
            <div class="text-xs text-fjord-fg-muted break-words">{e.message}</div>
          {/if}
        </div>
      </li>
    {/each}
  </ul>
{/if}
