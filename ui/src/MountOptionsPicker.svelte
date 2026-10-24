<script lang="ts">
  // A mount's options: a checkbox for each one this host offers by name, and
  // a box for any other podman option. Bound: `options` is the whole list.
  import { onMount } from 'svelte';
  import { offeredMountOptions, otherOptions, combine, withOption, type MountOption } from './mountOptions';

  export let options: string[] = [];

  let shown: MountOption[] = [{ name: 'ro', label: 'Read-only', help: '' }];
  let typed = '';
  onMount(async () => {
    shown = await offeredMountOptions();
    typed = otherOptions(options, shown);
  });

  function toggle(name: string, on: boolean) {
    const checked = on ? withOption(options, name) : options.filter((o) => o !== name);
    options = combine(checked, typed, shown);
  }
  function retype() {
    options = combine(options, typed, shown);
  }
</script>

<div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-fjord-fg-secondary">
  {#each shown as o (o.name)}
    <label class="flex items-center gap-1.5 cursor-pointer select-none" title={o.help}>
      <input
        type="checkbox"
        checked={options.includes(o.name)}
        on:change={(e) => toggle(o.name, e.currentTarget.checked)}
        class="accent-fjord-accent"
      />
      {o.label}
    </label>
  {/each}
  <input
    bind:value={typed}
    on:input={retype}
    placeholder="other options"
    title="Any other podman mount option, comma separated (nocopy, noexec, …)"
    aria-label="Other mount options"
    class="w-36 bg-fjord-inset border border-fjord-border rounded-lg px-2 py-1 text-xs font-mono text-fjord-fg-body focus:border-fjord-accent outline-none"
  />
</div>
