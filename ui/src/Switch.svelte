<script lang="ts">
  // An on/off switch: one control for one yes/no, where two buttons
  // ("Included" / "Left out") read as a choice between two things.
  import { createEventDispatcher } from 'svelte';
  export let checked = false;
  export let disabled = false;
  export let label = '';
  export let title = '';
  const dispatch = createEventDispatcher<{ change: boolean }>();
  function flip() {
    if (disabled) return;
    checked = !checked;
    dispatch('change', checked);
  }
</script>

<button
  type="button"
  role="switch"
  aria-checked={checked}
  aria-label={label}
  {title}
  {disabled}
  on:click={flip}
  class="relative inline-flex h-6 w-11 shrink-0 items-center rounded-full border transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-fjord-accent/60 disabled:opacity-40 disabled:cursor-not-allowed {checked
    ? 'bg-fjord-accent border-fjord-accent'
    : 'bg-fjord-inset border-fjord-border'}"
>
  <span
    class="inline-block h-[18px] w-[18px] rounded-full bg-white shadow transition-transform {checked ? 'translate-x-[22px]' : 'translate-x-[2px]'}"
  ></span>
</button>
