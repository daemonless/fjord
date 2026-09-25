<script lang="ts">
  // "Install" next to a doctor check fjord can fix itself (the check says
  // installable): runs its installer on the host, then asks the page to check
  // again. The copyable fix stays below it for anyone who would rather.
  import { createEventDispatcher } from 'svelte';
  import { toast } from './toast';
  import Spinner from './Spinner.svelte';

  export let id: string;
  export let name: string;

  const dispatch = createEventDispatcher<{ installed: void }>();
  let busy = false;

  async function install() {
    busy = true;
    try {
      const res = await fetch('/api/setup/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id }),
      });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      toast(`Installed: ${name}`, { kind: 'success' });
      dispatch('installed');
    } catch (e: any) {
      toast(`Could not install ${name}: ${e.message}`, { kind: 'error' });
    } finally {
      busy = false;
    }
  }
</script>

<button
  on:click={install}
  disabled={busy}
  class="shrink-0 flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-xs font-medium bg-fjord-accent text-white hover:bg-fjord-accent-hover transition-colors disabled:opacity-60"
  >{#if busy}<Spinner size={12} /> Installing…{:else}Install{/if}</button
>
