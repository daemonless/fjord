<script lang="ts">
  // The small shield beside a name link on a device that does not trust the
  // provider yet: the name opens, with a browser warning, until the device
  // is set up. One click says so and offers the trust page. Nothing is shown
  // once the device trusts it, or while that is still being checked.
  import { names, deviceTrust, trustPage, providerSettingsUrl } from './names';
  import Icon from './Icon.svelte';

  export let stacks: { name: string }[] = [];
  export let size = 12;

  let open = false;
  let root: HTMLElement;
  // The note is fixed to the viewport: the dashboard list clips what hangs
  // out of a row, and a row is one line tall.
  let at = { top: 0, left: 0 };
  $: show = $names.ready && ($deviceTrust === 'untrusted' || $deviceTrust === 'no-dns');

  function toggle() {
    if (!open) {
      const r = root.getBoundingClientRect();
      at = { top: r.bottom + 4, left: Math.min(r.left, window.innerWidth - 300) };
    }
    open = !open;
  }
</script>

<svelte:window on:click={(e) => open && !root?.contains(e.target as Node) && (open = false)} />

{#if show}
  <span class="relative inline-flex shrink-0" bind:this={root}>
    <button
      type="button"
      on:click|stopPropagation={toggle}
      title={$deviceTrust === 'no-dns' ? `This device can't find ${$names.domain} names yet` : `This device doesn't trust ${$names.provider} yet`}
      aria-label={$deviceTrust === 'no-dns' ? `This device can't find ${$names.domain} names yet` : `This device doesn't trust ${$names.provider} yet`}
      class="flex items-center text-fjord-warning hover:text-fjord-fg"><Icon name="shield" {size} /></button
    >
    {#if open}
      <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
      <div
        on:click|stopPropagation
        style="top:{at.top}px;left:{at.left}px"
        class="fixed z-30 w-72 bg-fjord-card border border-fjord-border rounded-lg shadow-xl p-3 text-xs text-fjord-fg-secondary font-sans whitespace-normal text-left cursor-default"
      >
        {#if $deviceTrust === 'no-dns'}
          <!-- Not a device thing: nothing on the network answers for the
               names. The fix is one record in the router, shown in the
               provider's Settings (by address: the name would not load). -->
          Nothing on your network answers for <span class="font-mono text-fjord-fg">*.{$names.domain}</span> yet, so names open direct here, without the proxy. One DNS record in your router fixes that for every device; {$names.provider}'s Settings shows it.
          <a
            href="{providerSettingsUrl($names, stacks)}#dns"
            target="_blank"
            rel="noreferrer"
            class="mt-2 block font-medium text-fjord-accent hover:underline">Add the DNS record</a
          >
        {:else}
          This device doesn't trust {$names.provider}'s certificate, so <span class="font-mono text-fjord-fg">{$names.domain}</span> names open here with a warning, or not at all. Trusting it is one step, once.
          <a
            href={trustPage($names, stacks, $deviceTrust)}
            target="_blank"
            rel="noreferrer"
            class="mt-2 block font-medium text-fjord-accent hover:underline">Set up this device</a
          >
        {/if}
      </div>
    {/if}
  </span>
{/if}
