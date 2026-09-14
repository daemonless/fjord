<script lang="ts">
  // Inline form for one NFS or SMB folder. Emits `add` with the finished row
  // (nfs://server/export, smb://user@server/share) after storing any SMB
  // password on the host; `cancel` closes it. Shared by the folder-set editor
  // and the stack Resources tab.
  import { createEventDispatcher } from 'svelte';
  import { buildRemoteRow, type RemoteKind } from './remote';
  import { toast } from './toast';

  export let kind: RemoteKind = 'nfs';

  const dispatch = createEventDispatcher<{ add: string; cancel: void }>();
  let server = '';
  let path = '';
  let user = '';
  let password = '';
  let busy = false;

  async function add() {
    busy = true;
    try {
      dispatch('add', await buildRemoteRow({ kind, server, path, user, password }));
    } catch (e: any) {
      toast(e.message || 'Could not add the folder', { kind: 'error' });
    } finally {
      busy = false;
    }
  }
</script>

<div class="p-2 rounded-md border border-fjord-border bg-fjord-inset/60 flex flex-wrap items-end gap-2 text-xs">
  <div class="flex flex-col gap-0.5"><span class="text-fjord-fg-dim">Server</span><input bind:value={server} placeholder="mars" class="w-28 bg-fjord-inset border border-fjord-border rounded px-2 py-1 text-fjord-fg-body font-mono focus:outline-none focus:border-fjord-accent" /></div>
  <div class="flex flex-col gap-0.5"><span class="text-fjord-fg-dim">{kind === 'nfs' ? 'Export path' : 'Share'}</span><input bind:value={path} placeholder={kind === 'nfs' ? '/mnt/media' : 'media'} class="w-36 bg-fjord-inset border border-fjord-border rounded px-2 py-1 text-fjord-fg-body font-mono focus:outline-none focus:border-fjord-accent" /></div>
  {#if kind === 'smb'}
    <div class="flex flex-col gap-0.5"><span class="text-fjord-fg-dim">User</span><input bind:value={user} placeholder="guest" autocomplete="off" class="w-24 bg-fjord-inset border border-fjord-border rounded px-2 py-1 text-fjord-fg-body font-mono focus:outline-none focus:border-fjord-accent" /></div>
    <div class="flex flex-col gap-0.5"><span class="text-fjord-fg-dim">Password</span><input type="password" bind:value={password} placeholder="stored root-only" autocomplete="new-password" class="w-28 bg-fjord-inset border border-fjord-border rounded px-2 py-1 text-fjord-fg-body font-mono focus:outline-none focus:border-fjord-accent" /></div>
  {/if}
  <button on:click={add} disabled={busy} class="bg-fjord-accent hover:bg-fjord-accent-hover text-white font-medium py-1 px-3 rounded disabled:opacity-50">Add</button>
  <button on:click={() => dispatch('cancel')} class="text-fjord-fg-muted hover:text-fjord-fg py-1 px-2">Cancel</button>
</div>
