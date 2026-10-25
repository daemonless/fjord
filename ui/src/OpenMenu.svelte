<script lang="ts">
  // The ▾ beside Open, once a names provider is set up: open by name or
  // direct, the app's own port at the host fjord was reached on (a check
  // on the one this browser uses; the last picked), and
  // Give names / Change name / Remove names. A stack of several apps has a
  // name per app; its other apps' list (▾ N) follows the same choice, and
  // Change name is for a stack of one app (one name to change).
  import { names, deviceTrust, openBy, nameTargets, nameLabel, providerUrl, addNames, removeName, renameName, freeLabels } from './names';
  import { toast } from './toast';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';

  export let stack: any;
  export let disabled = false;

  let open = false;
  let busy = false;
  let root: HTMLElement;
  // The menu as an editor for the one name: what is typed, and the label
  // the provider will get (nameLabel), shown when it differs.
  let editing = false;
  let draft = '';
  $: clean = nameLabel(draft);
  $: changed = clean !== '' && clean !== firstName?.name;
  const focus = (el: HTMLElement) => el.focus();

  $: targets = nameTargets(stack);
  // The provider itself has its own name (proxy.apps.lan), not one fjord
  // gave: By name / Direct only, nothing to give, change or remove.
  $: isProvider = !!stack && stack.name === $names.provider;
  $: ownUrl = isProvider ? providerUrl(stack, $names, 'name') : '';
  $: named = isProvider ? [] : targets.filter((t) => $names.names?.[t.key]);
  // Suggested with labels free on the provider (radarr-2 when another
  // fjord, or a hand-made host, holds radarr).
  $: missing = isProvider ? [] : freeLabels(targets.filter((t) => !$names.names?.[t.key]), $names);
  $: show = $names.ready && stack && targets.length > 0 && (!isProvider || ownUrl !== '');
  $: usesName = (named.length > 0 || ownUrl !== '') && $openBy === 'name';
  $: first = targets[0];
  $: firstName = first ? $names.names?.[first.key] : undefined;
  const bare = (u: string) => u.replace(/^https?:\/\//, '').replace(/\/$/, '');
  $: nameLine = ownUrl
    ? bare(ownUrl)
    : firstName
      ? bare(firstName.url) + (named.length > 1 ? ` and ${named.length - 1} more` : '')
      : `${named.length} names`;
  $: addrLine = first ? bare(first.url) + (targets.length > 1 ? ` and ${targets.length - 1} more` : '') : '';

  function close() {
    open = false;
    editing = false;
  }

  function edit() {
    draft = firstName?.name || first?.label || '';
    editing = true;
  }

  async function save() {
    if (!changed || !first || !firstName) return;
    const was = firstName.name;
    close();
    busy = true;
    try {
      const host = await renameName(first.key, draft);
      toast(`${was}.${$names.domain} is now ${host.name}.${$names.domain}`, {
        kind: 'success',
        actionLabel: 'Undo',
        timeout: 10000,
        onAction: () => renameName(first.key, was).catch((err: any) => toast(`Could not change the name back: ${err?.message || err}`, { kind: 'error' })),
      });
    } catch (err: any) {
      toast(`Could not change the name: ${err?.message || err}`, { kind: 'error' });
    } finally {
      busy = false;
    }
  }

  function pick(by: 'name' | 'address') {
    openBy.set(by);
    open = false;
    const url = by === 'name' ? ownUrl || firstName?.url || first?.url : first?.url;
    if (url) window.open(url, '_blank', 'noreferrer');
  }

  async function give() {
    open = false;
    busy = true;
    try {
      const results = await addNames(missing.map((target) => ({ target })));
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
          },
        });
      } else {
        toast(`No names: ${failed[0]?.error || `${$names.provider} refused them`}`, { kind: 'error' });
      }
    } catch (err: any) {
      toast(`No names: ${err?.message || err}`, { kind: 'error' });
    } finally {
      busy = false;
    }
  }

  async function remove() {
    open = false;
    const was = named.map((t) => ({ target: { ...t, label: $names.names?.[t.key]?.name || t.label } }));
    busy = true;
    try {
      await removeName(stack.name);
      toast(`${stack.displayName || stack.name} has no ${was.length === 1 ? 'name' : 'names'} now`, {
        actionLabel: 'Undo',
        timeout: 8000,
        onAction: () => addNames(was).catch(() => {}),
      });
    } catch (err: any) {
      toast(`Could not remove the names: ${err?.message || err}`, { kind: 'error' });
    } finally {
      busy = false;
    }
  }
</script>

<!-- Outside closes it, decided at pointerdown: by click time a menu entry
     that re-rendered the menu (Change name…) is no longer in the DOM, and
     a click test against root would close what was just opened. -->
<svelte:window on:pointerdown={(e) => open && !root?.contains(e.target as Node) && close()} on:keydown={(e) => open && e.key === 'Escape' && close()} />

{#if show}
  <span class="relative shrink-0 {disabled ? 'opacity-40 pointer-events-none' : ''}" bind:this={root}>
    <button
      on:click={() => (open ? close() : (open = true))}
      title="Open by name, or direct without the proxy"
      class="flex items-center text-sm font-medium text-fjord-accent hover:text-fjord-accent-hover"
      >{#if busy}<Spinner size={12} />{:else}▾{/if}</button
    >
    {#if open}
      <div class="absolute z-30 left-0 mt-1 min-w-80 bg-fjord-card border border-fjord-border rounded-lg shadow-xl py-1 text-sm">
        {#if editing && first && firstName}
          <form on:submit|preventDefault={save} class="flex flex-col gap-2 px-3 py-2">
            <label class="text-fjord-fg-secondary" for="rename-{first.key}">Open it at</label>
            <div class="flex items-center rounded-md border border-fjord-border bg-fjord-inset focus-within:border-fjord-accent">
              <span class="pl-3 text-fjord-fg-dim font-mono">https://</span>
              <input id="rename-{first.key}" bind:value={draft} use:focus spellcheck="false" autocomplete="off"
                class="min-w-0 flex-1 bg-transparent py-1.5 font-mono text-fjord-fg-body focus:outline-none" />
              <span class="pr-3 text-fjord-fg-dim font-mono">.{$names.domain}</span>
            </div>
            <div class="text-xs text-fjord-fg-dim">
              {#if clean === ''}Letters, digits and dashes.
              {:else if clean !== draft.trim()}Saved as <span class="font-mono">{clean}.{$names.domain}</span>.
              {:else if changed}<span class="font-mono">{firstName.name}.{$names.domain}</span> stops working.
              {:else}The name it has now.{/if}
            </div>
            <div class="flex justify-end gap-2">
              <button type="button" on:click={close} class="px-3 py-1 rounded-md text-fjord-fg-secondary hover:bg-fjord-border">Cancel</button>
              <button type="submit" disabled={!changed} class="px-3 py-1 rounded-md font-medium text-white bg-fjord-accent hover:bg-fjord-accent-hover disabled:opacity-40 disabled:cursor-not-allowed">Save</button>
            </div>
          </form>
        {:else}
        {#if named.length || ownUrl}
          <button on:click={() => pick('name')} class="w-full flex items-center gap-2 px-3 py-1.5 text-left hover:bg-fjord-border">
            <span class="w-4 shrink-0 text-fjord-accent">{#if usesName}<Icon name="check" size={13} />{/if}</span>
            <span class="text-fjord-fg-secondary">By name</span>
            <span class="font-mono text-fjord-fg-strong">{nameLine}</span>
            {#if $deviceTrust !== 'trusted'}
              <span class="ml-auto text-fjord-warning" title="This device does not trust {$names.provider} yet: the browser will warn until it does"
                ><Icon name="alert" size={13} /></span
              >
            {/if}
          </button>
          <button on:click={() => pick('address')} class="w-full flex items-center gap-2 px-3 py-1.5 text-left hover:bg-fjord-border">
            <span class="w-4 shrink-0 text-fjord-accent">{#if !usesName}<Icon name="check" size={13} />{/if}</span>
            <span class="text-fjord-fg-secondary">Direct</span>
            <span class="font-mono text-fjord-fg-secondary">{addrLine}</span>
          </button>
          {#if !isProvider}<div class="my-1 border-t border-fjord-border"></div>{/if}
        {/if}
        {#if missing.length}
          <button on:click={give} class="w-full flex items-center gap-2 px-3 py-1.5 text-left text-fjord-fg-secondary hover:bg-fjord-border">
            <span class="w-4 shrink-0"><Icon name="plus" size={12} /></span>
            {#if missing.length === 1}Give it a name like <span class="font-mono">{missing[0].label}.{$names.domain}</span>
            {:else}Give {named.length ? 'the other ' : ''}{missing.length} apps names like <span class="font-mono">{missing[0].label}.{$names.domain}</span>{/if}
          </button>
        {/if}
        {#if named.length === 1 && targets.length === 1}
          <button on:click={edit} class="w-full flex items-center gap-2 px-3 py-1.5 text-left text-fjord-fg-secondary hover:bg-fjord-border">
            <span class="w-4 shrink-0"><Icon name="pencil" size={12} /></span> Change name…
          </button>
        {/if}
        {#if named.length}
          <button on:click={remove} class="w-full flex items-center gap-2 px-3 py-1.5 text-left text-fjord-fg-secondary hover:bg-fjord-border">
            <span class="w-4 shrink-0"><Icon name="close" size={12} /></span> Remove {named.length === 1 ? 'name' : `${named.length} names`}
          </button>
        {/if}
        {/if}
      </div>
    {/if}
  </span>
{/if}
