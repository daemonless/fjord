<script lang="ts">
  // One line on the provider's own page, only while something is left to
  // do: finish its setup, or give the apps already installed their names.
  // The dashboard carries none of this; the row's link column says
  // "Finish setup" until the provider is ready, and nothing after.
  import { names, setupUrl, providerSettingsUrl, unnamed, addNames, removeName, freeLabels } from './names';
  import { toast } from './toast';
  import Spinner from './Spinner.svelte';
  import Icon from './Icon.svelte';

  export let stack: { name: string };
  export let stacks: any[] = [];

  let busy = false;
  $: mine = $names.provider === stack.name;
  $: todo = mine && $names.ready ? freeLabels(unnamed(stacks, $names), $names) : [];
  $: example = todo[0] ? `${todo[0].label}.${$names.domain}` : '';

  // Add names opens a list to pick from (all ticked): ten names in one
  // click, unseen, was too much. Unticked ones stay unnamed and are
  // offered again next time.
  let choosing = false;
  let skip = new Set<string>();
  $: chosen = todo.filter((t) => !skip.has(t.key));
  function toggle(key: string) {
    skip.has(key) ? skip.delete(key) : skip.add(key);
    skip = skip;
  }
  function open() {
    skip = new Set();
    choosing = true;
  }

  async function nameAll() {
    busy = true;
    try {
      const results = await addNames(chosen.map((target) => ({ target })));
      choosing = false;
      const made = results.filter((r) => r.host);
      const failed = results.filter((r) => !r.host);
      if (made.length) {
        const what = made.length === 1 ? `${made[0].host?.name}.${$names.domain} is ready` : `${made.length} apps have names`;
        toast(failed.length ? `${what} · ${failed.length} could not be reached` : what, {
          kind: 'success',
          actionLabel: 'Undo',
          timeout: 10000,
          onAction: async () => {
            for (const r of made) await removeName(r.stack).catch(() => {});
            toast('Names removed');
          },
        });
      } else {
        toast(`No names added: ${failed[0]?.error || `${stack.name} could not reach any app`}`, { kind: 'error' });
      }
    } catch (err: any) {
      toast(`No names added: ${err?.message || err}`, { kind: 'error' });
    } finally {
      busy = false;
    }
  }
</script>

{#if mine && $names.disconnected}
  <div class="flex items-center gap-3 rounded-lg border border-fjord-border bg-fjord-card px-4 py-2.5 text-sm" role="status">
    <Icon name="globe" size={14} class="shrink-0 text-fjord-fg-muted" />
    <span class="flex-1 min-w-0 text-fjord-fg-secondary">It disconnected fjord. Names you have still work; to add or change names, reconnect fjord in its Settings.</span>
    <a href={providerSettingsUrl($names, stacks)} target="_blank" rel="noreferrer" class="shrink-0 font-medium text-fjord-accent hover:underline">Open its Settings</a>
  </div>
{:else if mine && !$names.ready && !$names.setupToken && $names.error}
  <!-- Just installed: it does not answer yet. The row refreshes by itself. -->
  <div class="flex items-center gap-3 rounded-lg border border-fjord-border bg-fjord-card px-4 py-2.5 text-sm" role="status">
    <Spinner size={14} />
    <span class="flex-1 min-w-0 text-fjord-fg-secondary">Waiting for it to answer. Then one step: finish its setup, and your apps get names.</span>
  </div>
{:else if mine && !$names.ready && $names.setupToken}
  <div class="flex items-center gap-3 rounded-lg border border-fjord-border bg-fjord-card px-4 py-2.5 text-sm" role="status">
    <Icon name="globe" size={14} class="shrink-0 text-fjord-accent" />
    <span class="flex-1 min-w-0 text-fjord-fg-secondary">Finish setting it up and your apps get names like <span class="font-mono text-fjord-fg">radarr.apps.lan</span>. The link opens its setup verified; you pick a password and the ending.</span>
    <a href={setupUrl($names, stacks)} target="_blank" rel="noreferrer"
      class="shrink-0 rounded-lg bg-fjord-accent px-3 py-1 font-medium text-white hover:bg-fjord-accent-hover">Finish setup</a>
  </div>
{:else if todo.length}
  <div class="flex items-center gap-3 rounded-lg border border-fjord-border bg-fjord-card px-4 py-2.5 text-sm" role="status">
    <Icon name="globe" size={14} class="shrink-0 text-fjord-accent" />
    <span class="flex-1 min-w-0 text-fjord-fg-secondary"
      >{todo.length === 1 ? 'One app has' : `${todo.length} apps have`} no name yet. Like <span class="font-mono text-fjord-fg">{example}</span>, home network only.</span
    >
    {#if !choosing}
      <button on:click={open}
        class="shrink-0 rounded-lg bg-fjord-accent px-3 py-1 font-medium text-white hover:bg-fjord-accent-hover">{todo.length === 1 ? 'Add name…' : 'Add names…'}</button>
    {/if}
  </div>
  {#if choosing}
    <div class="rounded-lg border border-fjord-border bg-fjord-card text-sm" role="group" aria-label="Choose names">
      <div class="flex items-center gap-3 px-4 py-2.5 border-b border-fjord-border">
        <span class="flex-1 text-fjord-fg-secondary">Pick the apps to give a name. Each is reachable at its name on your home network only.</span>
        <button class="text-xs text-fjord-accent hover:underline" on:click={() => (skip = chosen.length === todo.length ? new Set(todo.map((t) => t.key)) : new Set())}
          >{chosen.length === todo.length ? 'None' : 'All'}</button>
      </div>
      <ul class="max-h-80 overflow-y-auto">
        {#each todo as t (t.key)}
          <li>
            <label class="flex items-center gap-3 px-4 py-2 hover:bg-fjord-inset cursor-pointer">
              <input type="checkbox" checked={!skip.has(t.key)} on:change={() => toggle(t.key)} class="accent-fjord-accent" />
              <span class="font-mono text-fjord-fg">{t.label}.{$names.domain}</span>
              <span class="ml-auto text-xs text-fjord-fg-dim font-mono truncate">{t.address}:{t.port}</span>
            </label>
          </li>
        {/each}
      </ul>
      <div class="flex items-center justify-end gap-3 px-4 py-2.5 border-t border-fjord-border">
        <button class="px-3 py-1 rounded-md text-fjord-fg-secondary hover:bg-fjord-border" on:click={() => (choosing = false)}>Cancel</button>
        <button on:click={nameAll} disabled={busy || chosen.length === 0}
          class="flex items-center gap-2 rounded-lg bg-fjord-accent px-3 py-1 font-medium text-white hover:bg-fjord-accent-hover disabled:opacity-50"
          >{#if busy}<Spinner size={12} />{/if} Add {chosen.length === 1 ? 'name' : `${chosen.length} names`}</button
        >
      </div>
    </div>
  {/if}
{/if}
