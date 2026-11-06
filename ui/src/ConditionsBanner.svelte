<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import FixSnippet from './FixSnippet.svelte';
  import { toast } from './toast';
  import { browserZone } from './setupPlan';
  import { loadSnoozed, SNOOZE_KEY, SNOOZE_MS, visibleConditions, type Condition } from './conditions';

  export let conditions: Condition[] = [];

  const dispatch = createEventDispatcher<{ changed: void }>();

  let snoozed: Record<string, number> =
    typeof localStorage !== 'undefined' ? loadSnoozed(localStorage.getItem(SNOOZE_KEY), Date.now()) : {};
  let expanded = false;
  let openSteps: Record<string, boolean> = {};
  let fixing: Record<string, boolean> = {};
  let fixOutput: Record<string, string> = {};
  let fixError: Record<string, string> = {};

  $: active = visibleConditions(conditions, snoozed, Date.now());
  $: shown = expanded ? active : active.slice(0, 1);
  $: extraCount = active.length > 1 ? active.length - 1 : 0;
  $: if (active.length <= 1) expanded = false;

  function snooze(id: string) {
    snoozed = { ...snoozed, [id]: Date.now() + SNOOZE_MS };
    try {
      localStorage.setItem(SNOOZE_KEY, JSON.stringify(snoozed));
    } catch {}
  }

  function toggleSteps(id: string) {
    openSteps = { ...openSteps, [id]: !openSteps[id] };
  }

  async function runFix(c: Condition) {
    fixing = { ...fixing, [c.id]: true };
    fixError = { ...fixError, [c.id]: '' };
    try {
      const res = await fetch('/api/conditions/fix', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: c.id, timezone: browserZone() }),
      });
      const data = await res.json().catch(() => ({}));
      if (data.output) fixOutput = { ...fixOutput, [c.id]: data.output };
      if (res.ok && data.fixed) {
        toast(`Fixed: ${c.title}`, { kind: 'success' });
        dispatch('changed');
      } else {
        const why = data.error || data.detail || `HTTP ${res.status}`;
        fixError = { ...fixError, [c.id]: why };
        openSteps = { ...openSteps, [c.id]: true };
        toast(`Could not fix: ${why}`, { kind: 'error' });
        dispatch('changed');
      }
    } catch (e: any) {
      const why = e?.message || 'fjordd did not answer';
      fixError = { ...fixError, [c.id]: why };
      openSteps = { ...openSteps, [c.id]: true };
      toast(`Could not fix: ${why}`, { kind: 'error' });
    } finally {
      fixing = { ...fixing, [c.id]: false };
    }
  }
</script>

{#if active.length > 0}
  <div class="shrink-0 border-b border-fjord-border bg-fjord-bg/95 pl-4 pr-14 sm:px-6 py-2 space-y-2 z-20">
    {#each shown as c (c.id)}
      <div
        class="rounded-lg border px-3.5 py-2.5 text-xs {c.severity === 'critical'
          ? 'bg-fjord-danger/10 border-fjord-danger/40 text-fjord-fg'
          : 'bg-fjord-warning/10 border-fjord-warning/40 text-fjord-fg'}"
      >
        <div class="flex items-start gap-2.5 flex-wrap">
          <span class="mt-0.5 shrink-0 {c.severity === 'critical' ? 'text-fjord-danger' : 'text-fjord-warning'}">
            <Icon name="alert" size={15} />
          </span>
          <div class="min-w-0 flex-1">
            <div class="font-semibold text-fjord-fg">{c.title}</div>
            {#if c.detail}
              <div class="text-fjord-fg-secondary mt-0.5">{c.detail}</div>
            {/if}
          </div>
          <div class="flex items-center gap-2 shrink-0 flex-wrap">
            {#if c.installable}
              <button
                on:click={() => runFix(c)}
                disabled={!!fixing[c.id]}
                class="flex items-center gap-1.5 px-2.5 py-1 rounded-md text-xs font-medium bg-fjord-accent hover:bg-fjord-accent-hover text-white disabled:opacity-60"
              >
                {#if fixing[c.id]}<Spinner size={12} /> Working…{:else}{c.action || 'Fix'}{/if}
              </button>
            {/if}
            {#if c.fix}
              <button
                on:click={() => toggleSteps(c.id)}
                class="px-2 py-1 rounded-md text-xs text-fjord-fg-secondary hover:text-fjord-fg border border-fjord-border hover:border-fjord-neutral"
              >
                {openSteps[c.id] ? 'Hide steps' : 'Steps'}
              </button>
            {/if}
            {#if c.severity !== 'critical'}
              <button
                on:click={() => snooze(c.id)}
                class="px-2 py-1 rounded-md text-xs text-fjord-fg-muted hover:text-fjord-fg"
              >
                Remind me tomorrow
              </button>
            {/if}
            {#if extraCount > 0 && c.id === shown[0].id}
              <button
                on:click={() => (expanded = !expanded)}
                class="px-2 py-1 rounded-md text-xs font-medium text-fjord-fg-secondary hover:text-fjord-fg bg-fjord-card border border-fjord-border"
              >
                {expanded ? 'Show fewer' : `+${extraCount} more`}
              </button>
            {/if}
          </div>
        </div>
        {#if fixError[c.id]}
          <div class="mt-2 ml-6 text-xs text-fjord-danger">Still not passing: {fixError[c.id]}</div>
        {/if}
        {#if openSteps[c.id]}
          <div class="mt-2 ml-6 space-y-1.5">
            {#if c.why}
              <p class="text-xs text-fjord-fg-dim">{c.why}</p>
            {/if}
            {#if fixOutput[c.id]}
              <pre class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-[11px] font-mono text-fjord-fg-secondary overflow-x-auto whitespace-pre-wrap">{fixOutput[c.id].trim()}</pre>
            {/if}
            {#if c.fix}
              <FixSnippet fix={c.fix} />
            {/if}
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}
