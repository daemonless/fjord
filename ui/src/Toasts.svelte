<script lang="ts">
  import { toasts, dismissToast } from './toast';
  import Icon from './Icon.svelte';
</script>

{#if $toasts.length}
  <div class="fixed bottom-6 inset-x-0 z-[70] flex flex-col items-center gap-2 pointer-events-none px-4">
    {#each $toasts as t (t.id)}
      <div
        class="pointer-events-auto flex items-center gap-3 max-w-lg rounded-full pl-4 pr-2 py-1.5 shadow-2xl border text-sm
          {t.kind === 'error'
          ? 'bg-fjord-card border-fjord-danger/40 text-fjord-fg-strong'
          : 'bg-fjord-card border-fjord-border text-fjord-fg-strong'}"
        role="status"
      >
        {#if t.kind === 'error'}
          <Icon name="alert" size={15} class="text-fjord-danger" />
        {:else if t.kind === 'success'}
          <Icon name="check" size={15} class="text-fjord-success" />
        {/if}
        <span class="max-w-md line-clamp-2" title={t.message}>{t.message}</span>
        {#if t.actionLabel}
          <button
            on:click={() => {
              t.onAction?.();
              dismissToast(t.id);
            }}
            class="shrink-0 px-2.5 py-1 rounded-full text-sm font-medium text-fjord-accent-hover hover:bg-fjord-border transition-colors"
            >{t.actionLabel}</button
          >
        {/if}
        <button
          on:click={() => dismissToast(t.id)}
          class="shrink-0 p-1.5 rounded-full text-fjord-fg-muted hover:text-fjord-fg hover:bg-fjord-border transition-colors"
          title="Dismiss"><Icon name="close" size={13} /></button
        >
      </div>
    {/each}
  </div>
{/if}
