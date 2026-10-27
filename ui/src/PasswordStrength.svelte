<script lang="ts">
  import { strength } from './password';

  // The meter under a new-password field: four bars, a word, and why.
  export let password: string;
  $: st = strength(password);
  $: color = ['', 'bg-fjord-danger', 'bg-fjord-warning', 'bg-fjord-success', 'bg-fjord-success'][st.level];
</script>

{#if st.level}
  <div class="mt-1.5 flex flex-col gap-0.5" aria-live="polite">
    <div class="flex items-center gap-3">
      <div class="flex gap-1 flex-1 max-w-48" aria-hidden="true">
        {#each [1, 2, 3, 4] as i (i)}<span class="h-1.5 flex-1 rounded-full {i <= st.level ? color : 'bg-fjord-border'}"></span>{/each}
      </div>
      <span class="text-xs font-semibold {st.level === 1 ? 'text-fjord-danger' : st.level === 2 ? 'text-fjord-warning' : 'text-fjord-success'}">{st.label}</span>
    </div>
    {#if st.why}<span class="text-xs text-fjord-fg-dim">{st.why}</span>{/if}
  </div>
{/if}
