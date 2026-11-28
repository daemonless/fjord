<script lang="ts">
  // The one-time offer to existing stacks, installed before Keep up to date
  // existed: on for all of them, or not, and not asked again either way.
  // Each stack's Settings tab can still change it.
  import { onMount } from 'svelte';
  import { toast } from './toast';

  let unasked: string[] = [];
  let start = '03:00';
  let end = '05:00';
  let busy = false;

  async function load() {
    try {
      const r = await fetch('/api/autoupdate');
      if (!r.ok) return;
      const d = await r.json();
      unasked = d.offer || [];
      start = d.start;
      end = d.end;
    } catch {
      // no offer shown
    }
  }
  onMount(load);

  async function answer(a: 'all' | 'no') {
    busy = true;
    try {
      const r = await fetch('/api/autoupdate/offer', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ answer: a }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
      const n = unasked.length;
      unasked = [];
      toast(
        a === 'all'
          ? n === 1
            ? 'Your app keeps itself up to date from tonight.'
            : `All ${n} apps keep themselves up to date from tonight.`
          : "Not turned on. Each app's Settings tab can turn it on.",
        { kind: 'success' },
      );
    } catch (e: any) {
      toast(`Keep up to date: ${e.message}`, { kind: 'error' });
    } finally {
      busy = false;
    }
  }
</script>

{#if unasked.length}
  <div
    data-autoupdate-offer
    class="flex flex-wrap items-center gap-3 mb-4 px-4 py-3 rounded-lg bg-fjord-accent/10 border border-fjord-accent/25 text-sm text-fjord-fg-body"
  >
    <span class="flex-1 min-w-[16rem]">
      <b>fjord can keep your apps up to date by itself.</b> Each night between {start} and {end} it installs rebuilds and new
      minor versions, one app at a time, and rolls back any that fails. New major versions still wait for you.
      <a href="#/settings/maintenance" class="text-fjord-accent hover:underline">Change the time</a>
    </span>
    <button
      on:click={() => answer('all')}
      disabled={busy}
      class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-accent text-white hover:bg-fjord-accent-hover transition-colors disabled:opacity-50"
      >Turn on for {unasked.length === 1 ? 'your app' : `all ${unasked.length} apps`}</button
    >
    <button
      on:click={() => answer('no')}
      disabled={busy}
      class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg transition-colors disabled:opacity-50"
      >No, thanks</button
    >
  </div>
{/if}
