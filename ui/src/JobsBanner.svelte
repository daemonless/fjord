<script lang="ts">
  import Spinner from './Spinner.svelte';
  import { jobsStore, cancelJob, dismissJob, jobPercent, jobStepLine, type BgJob } from './jobs';

  let expanded: Record<string, boolean> = {};
  let stoppingIds: Record<string, boolean> = {};

  function toggle(id: string) {
    expanded = { ...expanded, [id]: !expanded[id] };
  }

  async function onStop(id: string) {
    stoppingIds = { ...stoppingIds, [id]: true };
    await cancelJob(id);
    stoppingIds = { ...stoppingIds, [id]: false };
  }

  function dotClass(job: BgJob): string {
    if (job.status === 'failed') return 'bg-fjord-danger';
    if (job.status === 'stopped') return 'bg-fjord-warning';
    if (job.status === 'done') return 'bg-fjord-success';
    return 'bg-fjord-accent';
  }

  function barClass(job: BgJob): string {
    if (job.status === 'failed') return 'bg-fjord-danger';
    if (job.status === 'stopped') return 'bg-fjord-warning';
    if (job.status === 'done') return 'bg-fjord-success';
    return 'bg-fjord-accent';
  }

  function cardBorder(job: BgJob): string {
    if (job.status === 'failed') return 'border-fjord-danger/40 bg-fjord-danger/5';
    if (job.status === 'stopped') return 'border-fjord-warning/40 bg-fjord-warning/5';
    if (job.status === 'done') return 'border-fjord-success/40 bg-fjord-success/5';
    return 'border-fjord-accent/40 bg-fjord-card';
  }

  function itemBadge(status: string): { label: string; cls: string } {
    switch (status) {
      case 'done':
        return { label: 'Done', cls: 'text-fjord-success' };
      case 'running':
        return { label: 'Running', cls: 'text-fjord-accent font-medium' };
      case 'failed':
        return { label: 'Failed', cls: 'text-fjord-danger font-medium' };
      case 'skipped':
        return { label: 'Skipped', cls: 'text-fjord-fg-muted' };
      default:
        return { label: 'Queued', cls: 'text-fjord-fg-dim' };
    }
  }
</script>

{#if $jobsStore.length > 0}
  <div class="shrink-0 border-b border-fjord-border bg-fjord-bg/95 pl-4 pr-14 sm:px-6 py-2 space-y-2 z-20" data-jobs-banner>
    {#each $jobsStore as job (job.id)}
      {@const pct = jobPercent(job)}
      {@const items = job.items || []}
      {@const step = jobStepLine(job)}
      <div class="rounded-lg border px-3.5 py-2.5 text-xs {cardBorder(job)}" data-job-card={job.kind}>
        <div class="flex items-start justify-between gap-3 flex-wrap">
          <div class="flex items-start gap-2.5 min-w-0 flex-1">
            {#if job.status === 'running'}
              <span class="mt-0.5 shrink-0 text-fjord-accent"><Spinner size={13} /></span>
            {:else}
              <span class="mt-1.5 w-2 h-2 rounded-full shrink-0 {dotClass(job)}"></span>
            {/if}
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2 flex-wrap">
                <span class="font-semibold text-fjord-fg">{job.title}</span>
                {#if job.total > 0 && job.status !== 'stopped'}
                  <span class="font-mono text-[11px] text-fjord-fg-muted">
                    {#if job.status === 'running'}
                      {Math.min(job.done + 1, job.total)} of {job.total}
                    {:else}
                      {job.done} of {job.total}
                    {/if}
                  </span>
                {/if}
                {#if pct !== null && job.status === 'running'}
                  <span class="font-mono text-[11px] text-fjord-fg-dim">{pct}%</span>
                {/if}
              </div>
              {#if step}
                <div class="text-fjord-fg-secondary mt-0.5 truncate">{step}</div>
              {/if}
            </div>
          </div>
          <div class="flex items-center gap-2 shrink-0 flex-wrap">
            {#if items.length > 0}
              <button
                type="button"
                aria-expanded={!!expanded[job.id]}
                on:click={() => toggle(job.id)}
                class="px-2 py-1 rounded-md text-xs text-fjord-fg-secondary hover:text-fjord-fg border border-fjord-border hover:bg-fjord-border/40 transition-colors"
              >{expanded[job.id] ? 'Hide' : 'Details'}</button>
            {/if}
            {#if job.status === 'running' && job.cancellable}
              <button
                type="button"
                disabled={!!job.stopping || !!stoppingIds[job.id]}
                on:click={() => onStop(job.id)}
                class="px-2.5 py-1 rounded-md text-xs font-medium text-fjord-fg-secondary hover:text-fjord-fg border border-fjord-border bg-fjord-card hover:bg-fjord-border/40 transition-colors disabled:opacity-50"
              >{job.stopping ? 'Stopping after current…' : 'Stop after current'}</button>
            {/if}
            {#if job.status !== 'running'}
              <button
                type="button"
                on:click={() => dismissJob(job.id)}
                class="px-2.5 py-1 rounded-md text-xs text-fjord-fg-secondary hover:text-fjord-fg border border-fjord-border hover:bg-fjord-border/40 transition-colors"
              >Dismiss</button>
            {/if}
          </div>
        </div>

        <div
          class="mt-2 h-1.5 w-full rounded-full bg-fjord-border/70 overflow-hidden"
          role="progressbar"
          aria-label={job.title}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={pct ?? undefined}
        >
          {#if pct === null}
            <div class="h-full w-1/3 rounded-full bg-fjord-accent animate-pulse"></div>
          {:else}
            <div
              class="h-full rounded-full transition-all duration-300 {barClass(job)}"
              style="width: {pct}%"
            ></div>
          {/if}
        </div>

        {#if expanded[job.id] && items.length > 0}
          <ul class="mt-2.5 pt-2 border-t border-fjord-border/60 divide-y divide-fjord-border/40 max-h-48 overflow-y-auto">
            {#each items as item (item.name)}
              {@const badge = itemBadge(item.status)}
              <li class="py-1 flex items-center justify-between gap-3">
                <span class="font-medium text-fjord-fg-body truncate">{item.name}</span>
                <span class="flex items-center gap-2 shrink-0 text-[11px]">
                  {#if item.detail}
                    <span class="text-fjord-fg-muted max-w-[32ch] truncate" title={item.detail}>{item.detail}</span>
                  {/if}
                  <span class={badge.cls}>{badge.label}</span>
                </span>
              </li>
            {/each}
          </ul>
        {/if}
      </div>
    {/each}
  </div>
{/if}
