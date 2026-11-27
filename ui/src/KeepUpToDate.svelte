<script lang="ts">
  // A stack's "Keep up to date": on, fjord installs its rebuilds and new
  // minor versions in the update window by itself (policy "minor"); off,
  // updates wait for someone. Other policies set through the API show as
  // on, with their name.
  import { createEventDispatcher, onMount } from 'svelte';
  import Switch from './Switch.svelte';
  import { toast } from './toast';
  import { keepsUpToDate } from './autoupdate';

  export let name: string;
  export let policy: string | undefined;
  export let disabled = false;

  const dispatch = createEventDispatcher<{ changed: void }>();
  let saving = false;
  let start = '03:00';
  let end = '05:00';
  onMount(async () => {
    try {
      const r = await fetch('/api/autoupdate');
      if (r.ok) ({ start, end } = await r.json());
    } catch {
      // the defaults are what the daemon uses too
    }
  });
  $: on = keepsUpToDate(policy);

  async function set(next: boolean) {
    saving = true;
    try {
      const r = await fetch(`/api/stacks/${encodeURIComponent(name)}/policy`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ policy: next ? 'minor' : 'off' }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
      dispatch('changed');
    } catch (e: any) {
      toast(`Keep up to date: ${e.message}`, { kind: 'error' });
    } finally {
      saving = false;
    }
  }
</script>

<div class="flex flex-col gap-1.5" data-keep-up-to-date>
  <Switch checked={on} label="Keep up to date" disabled={disabled || saving} on:change={(e) => set(e.detail)} />
  <p class="text-xs text-fjord-fg-dim max-w-prose">
    {#if on}
      Between {start} and {end}, fjord installs its rebuilds, and new minor versions a day after it first sees them.
      It checks the app stays up and records each update in History. New major versions wait for you.
      {#if policy !== 'minor'}<span class="font-mono">({policy})</span>{/if}
    {:else}
      Updates wait for you. On, fjord installs rebuilds and new minor versions between {start} and {end} by itself.
    {/if}
  </p>
</div>
