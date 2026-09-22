<script lang="ts">
  // The form for mounting something into ONE service.
  //
  // It used to be a single box below the stack, and the compose writer it
  // called had no service parameter at all -- every mount went to whichever
  // service the file listed first. That is right for a stack with one service
  // and arbitrary for any other: adding storage to immich landed it on
  // immich-server because that is the order the file happens to be in.
  //
  // So the form belongs to the service, and the service comes with every
  // event it sends. Its own field state is local: two services being edited at
  // once are two forms, not one form with a target.
  import { createEventDispatcher } from 'svelte';
  import Icon from './Icon.svelte';
  import RemoteFolderForm from './RemoteFolderForm.svelte';

  export let service: string;
  /** appjail binds host paths via fstab and has no podman named volumes. */
  export let canUseVolumes = true;
  export let namedVolumes: { name: string; kind?: string }[] = [];
  export let folderSets: { id: string; name: string }[] = [];
  /** Bound so the host-path picker, which lives at the page level, can fill it. */
  export let source = '';

  let kind: 'bind' | 'volume' | 'remote' = 'bind';
  let remoteKind: 'nfs' | 'smb' = 'nfs';
  let dest = '';
  let readOnly = false;

  const dispatch = createEventDispatcher();
  const common = () => ({ service, dest: dest.trim(), readOnly });

  function submit() {
    if (!source.trim() || !dest.trim()) return;
    dispatch('add', { ...common(), kind, source: source.trim() });
  }
</script>

<div class="mt-2 p-3 rounded-lg bg-fjord-inset/40 border border-fjord-border">
  <div class="flex items-center justify-between mb-2">
    <div class="text-xs text-fjord-fg-muted">
      Mount into <span class="font-mono text-fjord-fg-body">{service}</span> — edits the compose,
      <b>Save</b> to apply.
    </div>
    <button
      on:click={() => dispatch('cancel')}
      class="text-[10px] px-1.5 py-0.5 rounded text-fjord-fg-muted hover:text-fjord-fg">Cancel</button
    >
  </div>

  <div class="flex flex-wrap items-end gap-3">
    {#if canUseVolumes}
      <div class="flex rounded-lg overflow-hidden border border-fjord-border shrink-0">
        <button
          on:click={() => { kind = 'bind'; source = ''; }}
          class="px-3 py-2 text-xs font-medium {kind === 'bind' ? 'bg-fjord-accent text-white' : 'bg-fjord-inset text-fjord-fg-muted hover:text-fjord-fg'}">Host path</button
        >
        <button
          on:click={() => { kind = 'volume'; source = ''; }}
          class="px-3 py-2 text-xs font-medium border-l border-fjord-border {kind === 'volume' ? 'bg-fjord-accent text-white' : 'bg-fjord-inset text-fjord-fg-muted hover:text-fjord-fg'}">Volume</button
        >
        <button
          on:click={() => { kind = 'remote'; source = ''; }}
          class="px-3 py-2 text-xs font-medium border-l border-fjord-border {kind === 'remote' ? 'bg-fjord-accent text-white' : 'bg-fjord-inset text-fjord-fg-muted hover:text-fjord-fg'}">NFS / SMB</button
        >
      </div>
    {/if}

    {#if kind === 'remote'}
      <!-- Remote folder: same form as the folder-set editor; mounted at the container path on the right. -->
      <div class="flex items-center gap-2">
        <select bind:value={remoteKind} class="bg-fjord-inset border border-fjord-border rounded-lg px-2 py-2 text-xs text-fjord-fg-secondary focus:border-fjord-accent outline-none">
          <option value="nfs">NFS</option>
          <option value="smb">SMB</option>
        </select>
        {#key remoteKind}
          <RemoteFolderForm
            kind={remoteKind}
            on:add={(e) => dispatch('remote', { ...common(), row: e.detail })}
            on:cancel={() => (kind = 'bind')}
          />
        {/key}
      </div>
    {:else if kind === 'volume'}
      <select
        bind:value={source}
        class="bg-fjord-inset border border-fjord-border rounded-lg px-3 py-2 text-sm text-fjord-fg-body focus:border-fjord-accent outline-none"
      >
        <option value="">Volume…</option>
        {#each namedVolumes as v}
          <option value={v.name}>{v.name}{v.kind && v.kind !== 'local' ? ` (${v.kind.toUpperCase()})` : ''}</option>
        {/each}
      </select>
    {:else}
      <div class="flex items-center gap-1.5">
        <input
          bind:value={source}
          placeholder="/host/path"
          class="w-52 bg-fjord-inset border border-fjord-border rounded-lg px-3 py-2 text-sm text-fjord-fg-body font-mono focus:border-fjord-accent outline-none"
        />
        <button
          on:click={() => dispatch('browse')}
          title="Browse the host filesystem"
          class="shrink-0 px-2.5 py-2 rounded-lg text-xs font-medium bg-fjord-inset border border-fjord-border text-fjord-fg-secondary hover:text-fjord-fg hover:border-fjord-accent/40 transition-colors">Browse…</button
        >
        {#if folderSets.length}
          <select
            aria-label="Add a folder set"
            title="Mounts the set's folders under the container path on the right"
            class="shrink-0 bg-fjord-inset border border-fjord-border rounded-lg px-2 py-2 text-xs text-fjord-fg-secondary focus:border-fjord-accent outline-none"
            on:change={(e) => { dispatch('folderset', { ...common(), id: e.currentTarget.value }); e.currentTarget.value = ''; }}
          >
            <option value="">Add folder set…</option>
            {#each folderSets as fs}<option value={fs.id}>{fs.name}</option>{/each}
          </select>
        {/if}
      </div>
    {/if}

    <Icon name="chevron-right" size={14} class="text-fjord-fg-faint shrink-0 mb-2.5" />
    <input
      bind:value={dest}
      placeholder="/container/path"
      class="w-48 bg-fjord-inset border border-fjord-border rounded-lg px-3 py-2 text-sm text-fjord-fg-body font-mono focus:border-fjord-accent outline-none"
    />
    <label class="flex items-center gap-1.5 text-sm text-fjord-fg-secondary cursor-pointer select-none mb-2">
      <input type="checkbox" bind:checked={readOnly} class="accent-fjord-accent" /> RO
    </label>
    {#if kind !== 'remote'}
      <button
        on:click={submit}
        disabled={!source.trim() || !dest.trim()}
        class="px-4 py-2 rounded-lg text-sm font-medium bg-fjord-border hover:bg-fjord-accent hover:text-white transition-colors disabled:opacity-40">Add</button
      >
    {/if}
  </div>
  {#if kind === 'volume' && namedVolumes.length === 0}
    <p class="text-xs text-fjord-fg-faint mt-2">No named volumes yet — create one on the Volumes page.</p>
  {/if}
</div>
