<script lang="ts">
  // Editor for one folder set's host folders: local paths (with Browse) and
  // remote nfs://server/export or smb://user@server/share rows. Shared by
  // Settings and the setup wizard so both get multi-folder + remote support.
  // Binds `folders`; fires `change` on any edit so a parent can track dirty.
  import { createEventDispatcher } from 'svelte';
  import Icon from './Icon.svelte';
  import DirPicker from './DirPicker.svelte';
  import RemoteFolderForm from './RemoteFolderForm.svelte';
  import { remoteKind, type RemoteKind } from './remote';

  export let folders: string[] = [];
  const dispatch = createEventDispatcher<{ change: void }>();
  const changed = () => {
    folders = folders;
    dispatch('change');
  };

  let pick: number | null = null; // DirPicker target row
  let remoteOpen: RemoteKind | null = null; // which remote form is showing
  function addFolder() {
    folders = [...folders, ''];
    changed();
  }
  function removeFolder(j: number) {
    folders = folders.filter((_, x) => x !== j);
    changed();
  }
  function addRemoteRow(row: string) {
    const cur = folders.filter((x) => x.trim());
    if (!cur.includes(row)) cur.push(row);
    folders = cur;
    remoteOpen = null;
    changed();
  }
</script>

<div class="space-y-1.5 mb-2 pl-1">
  {#each folders as _, j (j)}
    <div class="flex items-center gap-2">
      <input bind:value={folders[j]} on:input={changed} placeholder={'/mnt/movies or {{appdata}}/{{stack}}/data'} class="flex-1 min-w-0 bg-fjord-inset border border-fjord-border rounded-md px-2 py-1 text-xs text-fjord-fg-body font-mono focus:outline-none focus:border-fjord-accent" />
      {#if remoteKind(folders[j])}
        <span class="shrink-0 text-[10px] font-semibold uppercase tracking-wide text-fjord-fg-muted border border-fjord-border rounded px-1.5 py-0.5" title="Mounted as a named volume at install">{remoteKind(folders[j])}</span>
      {:else}
        <button on:click={() => (pick = j)} title="Browse" class="shrink-0 px-2 py-1 rounded-md text-xs bg-fjord-inset border border-fjord-border text-fjord-fg-secondary hover:text-fjord-fg">Browse…</button>
      {/if}
      <button on:click={() => removeFolder(j)} title="Remove folder" class="shrink-0 text-fjord-fg-dim hover:text-fjord-danger transition-colors"><Icon name="trash" size={13} /></button>
    </div>
  {/each}
  {#if folders.length === 0}
    <p class="text-xs text-fjord-fg-faint italic">No folders yet — add one below.</p>
  {/if}
</div>
<div class="flex items-center gap-3 pl-1">
  <button on:click={addFolder} class="text-xs text-fjord-fg-muted hover:text-fjord-fg flex items-center gap-1"><Icon name="plus" size={12} /> Add folder</button>
  <button on:click={() => (remoteOpen = 'nfs')} class="text-xs text-fjord-fg-muted hover:text-fjord-fg flex items-center gap-1"><Icon name="globe" size={12} /> Add NFS…</button>
  <button on:click={() => (remoteOpen = 'smb')} class="text-xs text-fjord-fg-muted hover:text-fjord-fg flex items-center gap-1"><Icon name="globe" size={12} /> Add SMB…</button>
</div>
{#if remoteOpen}
  <div class="mt-2 ml-1">
    <RemoteFolderForm kind={remoteOpen} on:add={(e) => addRemoteRow(e.detail)} on:cancel={() => (remoteOpen = null)} />
  </div>
{/if}
{#if pick !== null}
  <DirPicker
    start={folders[pick] || '/mnt'}
    on:select={(e) => {
      if (pick !== null) {
        folders[pick] = e.detail;
        changed();
      }
      pick = null;
    }}
    on:close={() => (pick = null)}
  />
{/if}
