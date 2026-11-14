<script lang="ts">
  import Icon from './Icon.svelte';
  // The engine a stack runs on, as a mark: podman's seal (its logo) or the
  // jail-bars glyph for appjail. Unknown engines get the generic stack glyph.
  export let engine: string;
  export let size = 14;
  export let strokeWidth = 2;
</script>

{#if engine === 'podman'}
  <img src="/engines/podman.svg" alt="" draggable="false" width={size} height={size} class="shrink-0" style="width:{size}px;height:{size}px" />
{:else if engine === 'appjail'}
  <Icon name="jail" {size} {strokeWidth} />
{:else}
  <Icon name="stack" {size} {strokeWidth} />
{/if}
