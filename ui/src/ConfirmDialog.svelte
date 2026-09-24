<script lang="ts">
  import { createEventDispatcher } from 'svelte';

  export let title = 'Are you sure?';
  export let message = '';
  export let confirmLabel = 'Confirm';
  export let danger = false;
  // When set, the user must type this exact text to enable the confirm button
  // (used for genuinely destructive actions like destroying data).
  export let confirmText = '';

  const dispatch = createEventDispatcher();
  let typed = '';
  $: ready = !confirmText || typed === confirmText;
</script>

<svelte:window
  on:keydown={(e) => {
    if (e.key === 'Escape') dispatch('cancel');
    // Enter confirms once ready -- for a type-to-confirm dialog that means the
    // typed text matches, so "type gitea, press Enter" deletes without the mouse.
    else if (e.key === 'Enter' && ready) dispatch('confirm');
  }}
/>

<!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
<div
  class="fixed inset-0 bg-black/70 backdrop-blur-sm flex items-center-safe justify-center overflow-y-auto z-50 p-4"
  on:click|self={() => dispatch('cancel')}
>
  <div class="bg-fjord-card border border-fjord-border rounded-xl shadow-2xl w-full max-w-md p-6">
    <h3 class="text-lg font-bold text-fjord-fg mb-2">{title}</h3>
    {#if message}<p class="text-fjord-fg-muted text-sm leading-relaxed mb-5">{message}</p>{/if}

    {#if confirmText}
      <p class="text-xs text-fjord-fg-muted mb-2">
        Type <span class="font-mono text-fjord-fg-body">{confirmText}</span> to confirm:
      </p>
      <!-- svelte-ignore a11y-autofocus -->
      <input
        bind:value={typed}
        autofocus
        class="w-full mb-5 bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent"
      />
    {/if}

    <div class="flex justify-end gap-3">
      <button
        on:click={() => dispatch('cancel')}
        class="px-4 py-2 rounded-md font-medium text-fjord-fg-secondary hover:bg-fjord-border transition-colors"
      >
        Cancel
      </button>
      <button
        on:click={() => dispatch('confirm')}
        disabled={!ready}
        class="px-4 py-2 rounded-md font-medium text-white transition-colors disabled:opacity-40 disabled:cursor-not-allowed {danger
          ? 'bg-fjord-danger hover:bg-fjord-danger-hover'
          : 'bg-fjord-accent hover:bg-fjord-accent-hover'}"
      >
        {confirmLabel}
      </button>
    </div>
  </div>
</div>
