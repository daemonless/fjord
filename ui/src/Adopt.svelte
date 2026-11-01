<script lang="ts">
  import { onMount, createEventDispatcher } from 'svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import EngineMark from './EngineMark.svelte';
  import { toast } from './toast';
  import { listCandidates, adoptContainer, startStack, type Candidate, isProject, startsAfterReplace } from './adopt';

  // Containers and jails no stack owns, and the stack each would become. The
  // daemon builds the preview from what the engine recorded (podman's run
  // line, appjail's jail settings), so what you see is exactly what gets
  // written.
  // adopted: the new stack to open ('' for none), and whether to start it.
  const dispatch = createEventDispatcher<{ adopted: { id: string; start: boolean }; back: void }>();

  // embedded: inside the setup wizard -- its own title, no way "back" to a
  // Stacks page it came from. initial: a list the caller already looked up,
  // shown at once instead of looking again.
  export let embedded = false;
  export let initial: Candidate[] | null = null;
  // An adopt is running: other adopt buttons, and the wizard's, wait.
  export let working = false;
  $: working = allBusy || !!busy;

  let list: Candidate[] = initial ?? [];
  let loading = initial === null;
  let open: Record<string, boolean> = {};
  let busy = '';

  async function load() {
    loading = true;
    list = await listCandidates();
    loading = false;
  }
  onMount(() => {
    if (initial === null) load();
  });

  // replace: remove the old container so the stack takes its name; the
  // stack is then started if the old container was running. Without replace
  // (a compose project's Import) its containers are already the stack's.
  // Everything adoptable, one after another: adopt+replace, then start the
  // ones that were running, so a host built by hand becomes stacks in one go. Failures are reported and
  // the run continues with the next container.
  let allBusy = false;
  let progress = '';
  // Adopt restarts a running container once, so it takes two clicks in
  // place -- the same shape as every other such button, not the browser's
  // confirm(). Adopting without replacing left a stopped copy beside the
  // still-running container, which is not what anyone means by Adopt.
  let confirmReplace = '';
  let confirmAll = false;
  async function adoptAll() {
    confirmAll = false;
    const todo = list.filter((c) => !c.error && !isProject(c));
    if (!todo.length) return;
    allBusy = true;
    let failed = 0;
    for (const [i, c] of todo.entries()) {
      progress = `${i + 1} / ${todo.length}: ${c.name}`;
      try {
        const id = await adoptContainer(c, true);
        if (startsAfterReplace(c)) await startStack(id);
      } catch (e: any) {
        failed++;
        toast(`${c.name}: ${e.message}`, { kind: 'error' });
      }
    }
    allBusy = false;
    progress = '';
    toast(failed ? `Adopted ${todo.length - failed}, ${failed} failed` : `Adopted ${todo.length} containers`, { kind: failed ? 'error' : 'success' });
    dispatch('adopted', { id: '', start: false });
    await load();
  }

  async function adopt(c: Candidate, replace: boolean) {
    confirmReplace = '';
    busy = c.name;
    try {
      const id = await adoptContainer(c, replace);
      const start = replace && startsAfterReplace(c);
      if (start && embedded) {
        // Nothing around the wizard starts it (the app opens the stack and
        // starts it there), so start it here and say if it did not.
        try {
          await startStack(id);
          toast(`Adopted ${c.name} — running as a stack`, { kind: 'success' });
        } catch (e: any) {
          toast(`Adopted ${c.name}, but it did not start: ${e.message}. Open it on the Stacks page.`, { kind: 'error', timeout: 12000 });
        }
      } else {
        toast(
          isProject(c)
            ? `Imported ${c.name} — fjord manages it now`
            : start
              ? `Adopted ${c.name} — starting`
              : `Adopted ${c.name} — stopped, as it was`,
          { kind: 'success' },
        );
      }
      dispatch('adopted', { id: replace ? id : '', start: start && !embedded });
      // The app moves on to the new stack; the wizard stays on this list.
      if (!replace || embedded) await load();
    } catch (e: any) {
      toast(`Adopt failed: ${e.message}`, { kind: 'error' });
    } finally {
      busy = '';
    }
  }
</script>

<div class="h-full flex flex-col">
  <div class="flex items-center justify-between gap-6 mb-4 shrink-0">
    <div class="max-w-3xl">
      <h2 class="text-2xl font-bold text-fjord-fg">{embedded ? 'Already running on this host' : 'Adopt existing containers'}</h2>
      <div class="text-sm text-fjord-fg-dim">
        Containers and jails on this host that no stack owns — started by hand, a script, or another tool.
        Adopting one turns what the engine recorded into a stack: same image, mounts, network address and name.
        {#if embedded}Nothing here has to be done now; the Stacks page can adopt them later.{/if}
      </div>
    </div>
    <div class="flex items-center gap-3">
      {#if list.filter((c) => !c.error).length > 1}
        {#if confirmAll}
          <span class="text-xs text-fjord-fg-secondary max-w-xs">
            Makes {list.filter((c) => !c.error && !isProject(c)).length} containers stacks with the same mounts,
            network address and name. The running ones restart once. Data stays where it is.
          </span>
          <button
            on:click={adoptAll}
            class="whitespace-nowrap bg-fjord-accent hover:bg-fjord-accent-hover text-white font-medium py-2 px-4 rounded-lg text-sm"
            >Adopt All</button
          >
          <button on:click={() => (confirmAll = false)} class="text-sm text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
        {:else}
          <button
            on:click={() => (confirmAll = true)}
            disabled={working}
            class="flex items-center gap-2 whitespace-nowrap bg-fjord-border hover:bg-fjord-accent hover:text-white text-fjord-fg-secondary font-medium py-2 px-4 rounded-lg text-sm disabled:opacity-50"
            >{#if allBusy}<Spinner size={13} /> {progress}{:else}Adopt all ({list.filter((c) => !c.error && !isProject(c)).length}){/if}</button
          >
        {/if}
      {/if}
      {#if !embedded}<button on:click={() => dispatch('back')} class="text-sm text-fjord-fg-muted hover:text-fjord-fg">← Stacks</button>{/if}
    </div>
  </div>

  <div class="flex-1 overflow-y-auto">
    {#if loading}
      <div class="flex items-center gap-3 text-fjord-fg-dim text-sm"><Spinner size={18} /> Looking for containers…</div>
    {:else if !list.length}
      <div class="text-sm text-fjord-fg-dim">Nothing to adopt — every container on this host already belongs to a stack.</div>
    {:else}
      <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border bg-fjord-card max-w-4xl">
        {#each list as c (c.engine + ':' + c.id)}
          <div class="px-4 py-3">
            <div class="flex items-center gap-3">
              <EngineMark engine={c.engine} size={16} />
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <span class="font-semibold text-fjord-fg">{c.name}</span>
                  <span class="text-[10px] font-semibold uppercase tracking-wide {c.state === 'running' ? 'text-fjord-success' : 'text-fjord-fg-dim'}">{c.state}</span>
                </div>
                {#if isProject(c)}
                  <div class="text-xs text-fjord-fg-dim truncate">
                    compose project · {(c.services ?? []).join(', ') || 'no services'} · <span class="font-mono">{c.workDir}</span>
                  </div>
                {:else}
                  <div class="text-xs text-fjord-fg-dim font-mono truncate">{c.image}</div>
                {/if}
                {#if c.error}<div class="text-xs text-fjord-danger mt-1">{c.error}</div>{/if}
                {#each c.notes || [] as n}<div class="text-xs text-fjord-warning mt-1">{n}</div>{/each}
              </div>
              <button on:click={() => (open[c.name] = !open[c.name])} class="text-xs text-fjord-fg-muted hover:text-fjord-fg px-2 py-1">
                {open[c.name] ? 'Hide' : 'Preview'}
              </button>
              {#if isProject(c)}
                <button
                  on:click={() => adopt(c, false)}
                  disabled={!!c.error || working}
                  title="Make this project a stack, from its own compose file; its containers keep running"
                  class="flex items-center gap-1.5 text-sm px-3 py-1.5 rounded-lg bg-fjord-accent hover:bg-fjord-accent-hover text-white disabled:opacity-40"
                  >{#if busy === c.name}<Spinner size={13} />{/if}Import</button>
              {:else}
              {#if confirmReplace === c.name}
                <button
                  on:click={() => adopt(c, true)}
                  class="text-sm px-3 py-1.5 rounded-lg bg-fjord-accent hover:bg-fjord-accent-hover text-white">{startsAfterReplace(c) ? 'Adopt & Restart' : 'Adopt'}</button>
                <button on:click={() => (confirmReplace = '')} class="text-sm px-2 py-1.5 text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
              {:else}
                <button
                  on:click={() => (confirmReplace = c.name)}
                  disabled={!!c.error || working}
                  title="Make it a stack fjord manages, running if it was"
                  class="flex items-center gap-1.5 text-sm px-3 py-1.5 rounded-lg bg-fjord-accent hover:bg-fjord-accent-hover text-white disabled:opacity-40"
                  >{#if busy === c.name}<Spinner size={13} />{/if}Adopt</button>
              {/if}
              {/if}
            </div>
            {#if confirmReplace === c.name}
              <div class="text-xs text-fjord-fg-secondary mt-2">
                {#if startsAfterReplace(c)}
                  <b>{c.name}</b> restarts once, as a stack with the same mounts, network address and name. Data stays where it is.
                {:else}
                  <b>{c.name}</b> becomes a stack with the same mounts, network address and name, stopped as it is now. Data stays where it is.
                {/if}
              </div>
            {/if}
            {#if open[c.name] && c.compose}
              <pre class="mt-3 text-xs bg-fjord-inset border border-fjord-border rounded-lg p-3 overflow-x-auto text-fjord-fg-secondary">{#if c.director}# appjail-director.yml
{c.director}
{c.makejail}
{c.template}
# compose.yaml
{/if}{c.compose}{#if c.env}
# .env
{c.env}{/if}</pre>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </div>
</div>
