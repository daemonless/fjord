<script lang="ts">
  import { onMount, createEventDispatcher } from 'svelte';
  import Icon from './Icon.svelte';

  export let name: string;
  export let image: string; // current image, e.g. ghcr.io/x/tautulli:pkg
  export let suggestTag = ''; // pre-select this tag (e.g. a newer version to upgrade to)

  const dispatch = createEventDispatcher();

  // Strip any @sha256:... digest first, then split repo:tag -- otherwise the
  // colon inside "sha256:" is mistaken for the tag separator.
  const bare = image.includes('@') ? image.slice(0, image.indexOf('@')) : image;
  const slash = bare.lastIndexOf('/');
  const colon = bare.lastIndexOf(':');
  const repo = colon > slash ? bare.slice(0, colon) : bare;
  const currentTag = colon > slash ? bare.slice(colon + 1) : 'latest';
  const currentlyPinned = image.includes('@sha256:');

  let trains: Record<string, { version: string; tag: string }[]> = {};
  let loading = true;
  let train = '';
  let versionTag = '';
  let customTag = '';
  let pin = currentlyPinned;
  let applying = false;

  $: trainList = Object.keys(trains);
  $: tag = customTag.trim() || versionTag || train;
  // Enable Apply when the version changed, or the pin state was toggled.
  $: dirty = tag !== currentTag || pin !== currentlyPinned;

  onMount(async () => {
    try {
      const res = await fetch(`/api/registry/versions?image=${encodeURIComponent(repo)}`);
      if (res.ok) trains = await res.json();
    } catch {}
    // Default the picker to the suggested tag (an upgrade target) if given,
    // else the currently-deployed tag.
    const target = suggestTag || currentTag;
    if (trains[target]) train = target;
    else {
      for (const [t, vs] of Object.entries(trains)) {
        if (vs.some((v) => v.tag === target)) {
          train = t;
          versionTag = target;
          break;
        }
      }
    }
    if (!train) train = trainList[0] || currentTag;
    loading = false;
  });

  function apply() {
    if (!tag) return;
    applying = true;
    dispatch('apply', { tag, pin });
  }
</script>

<svelte:window on:keydown={(e) => e.key === 'Escape' && dispatch('close')} />

<!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
<div
  class="fixed inset-0 bg-black/70 backdrop-blur-sm flex items-center justify-center z-50 p-4"
  on:click|self={() => dispatch('close')}
>
  <div class="bg-fjord-card border border-fjord-border rounded-xl shadow-2xl w-full max-w-md p-6">
    <h3 class="text-lg font-bold text-fjord-fg mb-1">Change Version — {name}</h3>
    <p class="text-fjord-fg-muted text-sm mb-5">
      Currently running <span class="font-mono text-fjord-fg-body">:{currentTag}</span>{#if currentlyPinned}
        <span class="text-fjord-accent font-medium inline-flex items-center gap-1"> · <Icon name="pin" size={11} /> pinned</span>{/if}
    </p>

    {#if loading}
      <p class="text-fjord-fg-dim text-sm">Loading published versions…</p>
    {:else if trainList.length === 0}
      <p class="text-fjord-fg-dim text-sm">No published tags found for this image.</p>
    {:else}
      <div class="grid grid-cols-2 gap-3">
        <div class="flex flex-col gap-1">
          <label class="text-sm font-semibold text-fjord-fg-secondary" for="cv-train">Build / channel</label>
          <select
            id="cv-train"
            bind:value={train}
            on:change={() => (versionTag = '')}
            class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
          >
            {#each trainList as t}<option value={t}>{t}</option>{/each}
          </select>
        </div>
        <div class="flex flex-col gap-1">
          <label class="text-sm font-semibold text-fjord-fg-secondary" for="cv-ver">Version</label>
          <select
            id="cv-ver"
            bind:value={versionTag}
            class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
          >
            <option value="">Latest (rolling)</option>
            {#each trains[train] || [] as v}<option value={v.tag}>{v.version}</option>{/each}
          </select>
        </div>
      </div>
      <div class="flex items-center gap-2 mt-3">
        <input
          bind:value={customTag}
          placeholder="Custom tag (optional)"
          class="flex-1 bg-fjord-inset border border-fjord-border rounded-md px-3 py-1.5 text-fjord-fg-body font-mono text-xs focus:outline-none focus:border-fjord-accent"
        />
        <span class="text-xs text-fjord-fg-dim shrink-0">→ <span class="font-mono text-fjord-fg-secondary">:{tag}</span></span>
      </div>
    {/if}

    {#if !loading}
      <label class="flex items-start gap-2 mt-4 cursor-pointer select-none">
        <input type="checkbox" bind:checked={pin} class="mt-0.5 accent-fjord-accent" />
        <span class="text-sm text-fjord-fg-secondary">
          Pin to exact image <span class="font-mono text-xs text-fjord-fg-dim">(sha256 digest)</span>
          <span class="block text-xs text-fjord-fg-dim">
            Locks to the exact <span class="font-mono text-fjord-fg-muted">sha256:…</span> that
            <span class="font-mono text-fjord-fg-muted">:{tag}</span> points to right now, so if that tag is later
            re-published to a different image, this stack stays on the bytes you pinned. Update won't move it
            until you unpin.
          </span>
        </span>
      </label>
    {/if}

    <div class="flex justify-end gap-3 mt-6">
      <button
        on:click={() => dispatch('close')}
        class="px-4 py-2 rounded-md font-medium text-fjord-fg-secondary hover:bg-fjord-border transition-colors">Cancel</button
      >
      <button
        on:click={apply}
        disabled={applying || !tag || !dirty}
        class="px-4 py-2 rounded-md font-medium text-white bg-fjord-accent hover:bg-fjord-accent-hover transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
        >{applying ? 'Applying…' : tag !== currentTag ? 'Change & Redeploy' : pin ? 'Pin' : 'Unpin'}</button
      >
    </div>
  </div>
</div>
