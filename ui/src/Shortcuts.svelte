<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import { shortcuts, alsoShortcuts } from './shortcuts';
  export let version = '';
  const dispatch = createEventDispatcher();
  const keys = (k: string) => k.split(' ');
</script>

<!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
<div
  class="fixed inset-0 bg-black/70 backdrop-blur-sm flex items-center-safe justify-center overflow-y-auto z-50 p-4"
  on:click|self={() => dispatch('close')}
  role="dialog"
  aria-label="Keyboard shortcuts"
  tabindex="-1"
>
  <div class="bg-fjord-card border border-fjord-border rounded-xl shadow-2xl w-full max-w-md p-6">
    <div class="flex items-start justify-between mb-4">
      <h3 class="text-lg font-bold text-fjord-fg">Keyboard shortcuts</h3>
      <button on:click={() => dispatch('close')} class="text-fjord-fg-muted hover:text-fjord-fg text-xl leading-none" aria-label="Close">×</button>
    </div>
    <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
      {#each [...shortcuts(), ...alsoShortcuts()] as s}
        <dt class="flex gap-1 whitespace-nowrap">
          {#each keys(s.keys) as k}<kbd class="px-1.5 py-0.5 rounded border border-fjord-border bg-fjord-inset font-mono text-xs text-fjord-fg-body">{k}</kbd>{/each}
        </dt>
        <dd class="text-fjord-fg-secondary">{s.does}</dd>
      {/each}
    </dl>
    <div class="mt-5 pt-4 border-t border-fjord-border flex items-center justify-between text-xs text-fjord-fg-muted">
      <span>{version ? `fjord ${version}` : 'fjord'}</span>
      <a href="https://daemonless.io/" target="_blank" rel="noopener" class="text-fjord-accent hover:underline">Docs</a>
    </div>
  </div>
</div>
