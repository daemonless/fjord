<script lang="ts">
  import { createEventDispatcher, onMount } from 'svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import { toast } from './toast';

  export let start = '/containers';
  const dispatch = createEventDispatcher();

  let path = start;
  let parent = '';
  let entries: { name: string; isDir: boolean }[] = [];
  let loading = true;
  let error = '';
  let newFolder = '';
  let creating = false;

  async function load(p: string) {
    loading = true;
    error = '';
    try {
      const res = await fetch(`/api/browse?path=${encodeURIComponent(p)}`);
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      const d = await res.json();
      path = d.path;
      parent = d.parent;
      entries = d.entries;
      // If the server climbed to an existing ancestor (the requested dir doesn't
      // exist yet), pre-fill the missing tail so one click creates + selects it.
      if (d.requested && d.requested !== d.path) {
        newFolder = d.requested.slice(d.path.replace(/\/$/, '').length).replace(/^\/+/, '');
      }
    } catch (e: any) {
      error = e.message;
      entries = [];
    } finally {
      loading = false;
    }
  }

  onMount(() => load(start));

  async function makeFolder() {
    const name = newFolder.trim();
    if (!name) return;
    creating = true;
    try {
      const res = await fetch('/api/browse', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path, name }),
      });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      const d = await res.json();
      newFolder = '';
      await load(d.path); // navigate into the new folder
    } catch (e: any) {
      toast(e.message || 'Could not create folder', { kind: 'error' });
    } finally {
      creating = false;
    }
  }
</script>

<!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
<div class="fixed inset-0 bg-black/70 backdrop-blur-sm flex items-center-safe justify-center overflow-y-auto z-[60] p-4" on:click|self={() => dispatch('close')}>
  <div class="bg-fjord-card border border-fjord-border rounded-xl shadow-2xl w-full max-w-lg flex flex-col" style="max-height: 80vh">
    <div class="p-4 border-b border-fjord-border">
      <h3 class="text-base font-bold text-fjord-fg mb-1">Choose a directory</h3>
      <div class="text-xs font-mono text-fjord-fg-muted truncate" title={path}>{path}</div>
    </div>

    <div class="flex-1 overflow-y-auto min-h-40">
      {#if parent}
        <button on:click={() => load(parent)} class="w-full flex items-center gap-2 px-4 py-2 text-sm text-fjord-fg-secondary hover:bg-fjord-border transition-colors">
          <Icon name="chevron-up" size={14} class="text-fjord-fg-dim" /> <span class="font-mono">..</span>
        </button>
      {/if}
      {#if loading}
        <div class="flex items-center gap-2 px-4 py-3 text-fjord-fg-dim text-sm"><Spinner size={16} /> Loading…</div>
      {:else if error}
        <div class="px-4 py-3 text-xs text-fjord-fg-dim">{error} — you can still create it below and select it.</div>
      {:else if entries.length === 0}
        <div class="px-4 py-3 text-xs text-fjord-fg-faint italic">No sub-directories.</div>
      {:else}
        {#each entries as e}
          <button on:click={() => load(`${path.replace(/\/$/, '')}/${e.name}`)} class="w-full flex items-center gap-2 px-4 py-2 text-sm text-fjord-fg-body hover:bg-fjord-border transition-colors">
            <Icon name="drive" size={14} class="text-fjord-fg-dim shrink-0" /> <span class="truncate">{e.name}</span>
          </button>
        {/each}
      {/if}
    </div>

    <div class="p-3 border-t border-fjord-border flex items-center gap-2">
      <input
        bind:value={newFolder}
        on:keydown={(e) => { if (e.key === 'Enter') makeFolder(); }}
        placeholder="new folder name"
        class="flex-1 min-w-0 bg-fjord-inset border border-fjord-border rounded-md px-3 py-1.5 text-sm text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
      />
      <button on:click={makeFolder} disabled={creating || !newFolder.trim()} title="Create folder here" class="shrink-0 flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm bg-fjord-border hover:bg-fjord-border/70 text-fjord-fg-body disabled:opacity-50">
        <Icon name="plus" size={13} /> New
      </button>
    </div>

    <div class="p-3 border-t border-fjord-border flex justify-end gap-2">
      <button on:click={() => dispatch('close')} class="px-4 py-2 rounded-md text-sm font-medium text-fjord-fg-secondary hover:text-fjord-fg hover:bg-fjord-border transition-colors">Cancel</button>
      <button on:click={() => dispatch('select', path)} class="bg-fjord-accent hover:bg-fjord-accent-hover text-white text-sm font-medium py-2 px-4 rounded-lg">Select this folder</button>
    </div>
  </div>
</div>
