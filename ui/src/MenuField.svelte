<script lang="ts">
  // A form field that opens a menu of choices: the button that shows the
  // current one, the menu under it, and the keys (Escape closes, arrows
  // move between choices). The choices are the caller's, as
  // role="menuitemradio"; the caller closes it when it is disabled.
  import { tick } from 'svelte';
  import { fitMenu } from './fitMenu';

  export let label: string;
  export let title = '';
  export let disabled = false;
  export let open = false;
  /** Id of the visible line that says why the field is locked. */
  export let describedBy = '';
  /** data-* attributes for the button. */
  export let buttonAttrs: Record<string, string> = {};

  let button: HTMLButtonElement;
  let menu: HTMLElement;

  const items = () => [...(menu?.querySelectorAll<HTMLElement>('[role="menuitemradio"]:not(:disabled)') ?? [])];

  async function toggle() {
    open = !open;
    if (!open) return;
    await tick();
    const all = items();
    (all.find((i) => i.getAttribute('aria-checked') === 'true') ?? all[0])?.focus();
  }

  function close() {
    open = false;
    button?.focus();
  }

  function keys(e: KeyboardEvent) {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const at = e.target as HTMLElement;
    if (at.getAttribute('role') !== 'menuitemradio') return;
    const all = items();
    const i = all.indexOf(at);
    e.preventDefault();
    all[(i + (e.key === 'ArrowDown' ? 1 : all.length - 1)) % all.length]?.focus();
  }
</script>

<svelte:window on:keydown={(e) => open && e.key === 'Escape' && close()} />

<div class="relative shrink-0" {...$$restProps}>
  <button
    bind:this={button}
    type="button"
    on:click={toggle}
    {disabled}
    aria-haspopup="menu"
    aria-expanded={open}
    aria-describedby={describedBy || undefined}
    title={title || undefined}
    {...buttonAttrs}
    class="flex items-center justify-between gap-3 w-full sm:w-auto sm:min-w-56 bg-fjord-inset border border-fjord-border rounded-lg px-2.5 py-1 text-sm text-fjord-fg-body hover:border-fjord-fg-faint focus:outline-none focus:border-fjord-accent disabled:opacity-50 disabled:hover:border-fjord-border"
    ><span>{label}</span><span aria-hidden="true" class="text-fjord-fg-dim">▾</span></button
  >
  {#if open}
    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
    <div class="fixed inset-0 z-20" on:click={close}></div>
    <!-- svelte-ignore a11y-no-noninteractive-element-interactions -->
    <div
      bind:this={menu}
      role="menu"
      tabindex="-1"
      use:fitMenu
      on:keydown={keys}
      class="absolute z-30 left-0 mt-1 w-96 bg-fjord-card border border-fjord-border rounded-lg shadow-xl py-1"
    >
      <slot />
    </div>
  {/if}
</div>
