<script lang="ts">
  // The first setup screen, one thing at a time: pick the engines, let fjord
  // do its part (listed, then run with a live terminal), then the user's part
  // -- terminal commands that change the host's own configuration -- in a card
  // of its own, then ready. Everything that already passed stays folded away.
  import { onMount } from 'svelte';
  import Icon from './Icon.svelte';
  import EngineMark from './EngineMark.svelte';
  import Spinner from './Spinner.svelte';
  import FixSnippet from './FixSnippet.svelte';
  import Terminal from './Terminal.svelte';
  import { toast } from './toast';
  import { plan, fetchChecks, purpose, installStream, type Check, type Plan } from './setupPlan';

  // The first engine picked, in list order: the default for new installs.
  export let engineChoice = '';
  type Engine = { name: string; description?: string; available: boolean; enabled: boolean; default: boolean; warning?: string };
  export let engines: Engine[] = [];

  let checks: Check[] = [];
  let loading = true;
  let loadedOnce = false;
  let running = false;
  let selected: string[] = [];

  $: names = engines.map((e) => e.name);
  $: p = plan(checks, selected, names) as Plan;
  $: engineChoice = names.find((n) => selected.includes(n)) || '';
  const label = (n: string) => (n === 'appjail' ? 'AppJail' : n);

  async function load() {
    loading = true;
    try {
      const r = await fetch('/api/engine');
      if (r.ok) engines = (await r.json()).engines || [];
      // Start with what is already installed ticked, and nothing else:
      // fjord has no favourite.
      if (!loadedOnce) selected = engines.filter((e) => e.enabled).map((e) => e.name);
      loadedOnce = true;
      checks = selected.length ? (await fetchChecks(selected)).checks : [];
    } catch (e: any) {
      toast(`Could not check the host: ${e.message}`, { kind: 'error' });
    }
    loading = false;
  }
  async function toggle(name: string) {
    if (running) return;
    selected = selected.includes(name) ? selected.filter((n) => n !== name) : [...selected, name];
    ran = [];
    term = '';
    await load();
  }

  // What this run did keeps its tick after the re-check drops it from the plan.
  type Step = Check & { state: 'todo' | 'running' | 'done' | 'failed'; error?: string };
  let ran: Step[] = [];
  $: steps = [...ran, ...p.auto.filter((c) => !ran.some((r) => r.id === c.id)).map((c): Step => ({ ...c, state: 'todo' }))];
  $: failedRun = ran.some((r) => r.state === 'failed');
  $: doneCount = ran.filter((r) => r.state === 'done').length;

  // The terminal: every command and its output, live, behind "Show output"
  // (which opens itself when a step fails).
  let term = '';
  let termStatus: 'idle' | 'running' | 'error' = 'idle';
  let showCommands = false;
  let showOutput = false;
  let showAll = false;
  $: if (failedRun) showOutput = true;

  async function setUpAll() {
    running = true;
    termStatus = 'running';
    term = '';
    ran = p.auto.map((c): Step => ({ ...c, state: 'todo' }));
    for (const st of ran) {
      st.state = 'running';
      ran = ran;
      term += `\x1b[1m# ${st.action || st.name}\x1b[0m\n`;
      const err = await installStream(st.id, (t) => (term += t));
      term += '\n';
      if (err) {
        st.state = 'failed';
        st.error = err;
        ran = ran;
        termStatus = 'error';
        break;
      }
      st.state = 'done';
      ran = ran;
    }
    if (termStatus !== 'error') termStatus = 'idle';
    running = false;
    await load();
  }

  let rechecking = false;
  async function recheck() {
    rechecking = true;
    await load();
    rechecking = false;
  }

  onMount(load);
</script>

{#if loading && !loadedOnce}
  <div class="flex items-center gap-2 text-sm text-fjord-fg-dim"><Spinner size={14} /> Checking the host…</div>
{:else}
  <p class="text-sm text-fjord-fg-body mb-3">Apps run in FreeBSD jails, with podman, AppJail, or both. Pick what you want:</p>
  <div class="grid sm:grid-cols-2 gap-2 mb-6">
    {#each engines as e (e.name)}
      <label
        class="flex items-center gap-3 p-3 rounded-xl border cursor-pointer transition-colors {selected.includes(e.name)
          ? 'border-fjord-accent bg-fjord-accent/10'
          : 'border-fjord-border hover:border-fjord-neutral'}"
      >
        <input type="checkbox" checked={selected.includes(e.name)} on:change={() => toggle(e.name)} disabled={running} class="accent-fjord-accent" />
        <EngineMark engine={e.name} size={18} />
        <span class="min-w-0">
          <span class="block font-semibold text-fjord-fg">{label(e.name)}</span>
          <span class="block text-xs text-fjord-fg-muted">{e.enabled ? 'installed' : 'not installed yet'}</span>
        </span>
      </label>
    {/each}
  </div>

  {#if !selected.length}
    <p class="text-sm text-fjord-fg-dim">Pick at least one.</p>
  {:else if p.auto.length || running || (ran.length && !p.manual.length && !p.ready)}
    <!-- fjord's part -->
    <div class="rounded-xl border border-fjord-border p-4">
      <div class="flex items-center justify-between gap-3 mb-2">
        <h3 class="text-sm font-semibold text-fjord-fg">
          {#if running}Setting up… {doneCount} of {ran.length}{:else if failedRun}Setup stopped{:else}fjord sets up {p.auto.length} thing{p.auto.length === 1 ? '' : 's'}{/if}
        </h3>
        <button class="text-xs text-fjord-fg-muted hover:text-fjord-fg" on:click={() => (showCommands = !showCommands)}>{showCommands ? 'Hide commands' : 'Show commands'}</button>
      </div>
      {#if running}
        <div class="h-1.5 rounded-full bg-fjord-border overflow-hidden mb-3">
          <div class="h-full bg-fjord-accent transition-all" style="width: {ran.length ? Math.round((doneCount / ran.length) * 100) : 0}%"></div>
        </div>
      {/if}
      <ul class="space-y-1 mb-3">
        {#each steps as st (st.id)}
          <li class="flex items-start gap-2.5 text-sm">
            <span class="w-4 mt-0.5 shrink-0 flex justify-center">
              {#if st.state === 'running'}<Spinner size={13} />
              {:else if st.state === 'done'}<Icon name="check" size={14} class="text-fjord-success" />
              {:else if st.state === 'failed'}<Icon name="alert" size={14} class="text-fjord-danger" />
              {:else}<span class="w-2.5 h-2.5 mt-0.5 rounded-full border border-fjord-fg-dim"></span>{/if}
            </span>
            <span class="min-w-0">
              <span class="{st.state === 'done' ? 'text-fjord-fg-muted' : 'text-fjord-fg-body'}">{st.action || st.name}</span>
              {#if showCommands && st.commands?.length}
                <pre class="mt-0.5 text-[11px] font-mono leading-4 text-fjord-fg-muted whitespace-pre-wrap break-all">{#each st.commands as cmd}{#if cmd.startsWith('#')}<span class="text-fjord-fg-dim">{cmd}</span>{:else}<span class="text-fjord-fg-dim select-none">$ </span>{cmd}{/if}
{/each}</pre>
              {/if}
              {#if st.error}<span class="block text-xs text-fjord-danger mt-0.5">{st.error}</span>{/if}
            </span>
          </li>
        {/each}
      </ul>
      {#if !running}
        <div class="flex items-center gap-3">
          <button
            on:click={setUpAll}
            disabled={rechecking}
            class="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-fjord-accent hover:bg-fjord-accent-hover text-white disabled:opacity-60"
            >{failedRun ? 'Try again' : 'Set up'}</button
          >
          <!-- For anyone who ran the commands themselves. -->
          <button
            on:click={recheck}
            disabled={rechecking}
            class="flex items-center gap-1.5 px-3 py-2 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg border border-fjord-border hover:border-fjord-neutral disabled:opacity-60"
            >{#if rechecking}<Spinner size={13} />{:else}<Icon name="refresh" size={13} />{/if} Check again</button
          >
        </div>
      {/if}
      {#if p.manual.length && !failedRun}
        <p class="text-xs text-fjord-fg-dim mt-3">
          After this, {p.manual.length === 1 ? 'one thing' : `${p.manual.length} things`} for you to run in a terminal.
        </p>
      {/if}
      {#if term}
        <button class="flex items-center gap-1 text-xs text-fjord-fg-muted hover:text-fjord-fg mt-3" on:click={() => (showOutput = !showOutput)}>
          <Icon name={showOutput ? 'chevron-down' : 'chevron-right'} size={12} /> {showOutput ? 'Hide output' : 'Show output'}
        </button>
        {#if showOutput}
          <div class="mt-2 h-64 flex flex-col">
            <Terminal logs={term} status={termStatus} statusMessage={termStatus === 'error' ? 'A step failed' : ''} />
          </div>
        {/if}
      {/if}
    </div>
  {:else if p.manual.length}
    <!-- your part: its own card, unmistakably a terminal job -->
    <div class="rounded-xl border-2 border-fjord-warning/50 bg-fjord-warning/5 p-4">
      <div class="flex items-center gap-2 mb-1">
        <Icon name="terminal" size={16} class="text-fjord-warning" />
        <h3 class="text-sm font-semibold text-fjord-fg">Your turn: {p.manual.length === 1 ? 'one thing' : `${p.manual.length} things`} to run in a terminal</h3>
      </div>
      <p class="text-xs text-fjord-fg-muted mb-4">
        {#if ran.length}fjord did its part. {/if}These change your host's own configuration, so fjord leaves them to you. Run them as root on this host.
      </p>
      <ol class="space-y-4">
        {#each p.manual as c, i (c.id)}
          <li>
            <div class="flex items-baseline gap-2">
              <span class="text-xs font-semibold text-fjord-warning">{i + 1}.</span>
              <span class="text-sm font-medium text-fjord-fg">{c.name}</span>
            </div>
            <p class="text-xs text-fjord-fg-dim mt-0.5 mb-2 ml-5">{purpose(c)}.</p>
            {#if c.fix}<div class="ml-5"><FixSnippet fix={c.fix} /></div>{/if}
          </li>
        {/each}
      </ol>
      <button
        on:click={recheck}
        disabled={rechecking}
        class="mt-4 flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium border border-fjord-warning/60 text-fjord-fg hover:bg-fjord-warning/10 disabled:opacity-60"
        >{#if rechecking}<Spinner size={13} />{/if}I've done it, check again</button
      >
    </div>
  {:else if p.ready}
    <div class="flex items-center gap-3 p-4 rounded-xl bg-fjord-success/10 border border-fjord-success/30">
      <Icon name="check" size={20} class="text-fjord-success" />
      <div class="text-sm text-fjord-fg">Ready to run apps with {selected.map(label).join(' and ')}.</div>
    </div>
  {/if}

  {#if selected.length}
    <button class="flex items-center gap-1 text-xs text-fjord-fg-muted hover:text-fjord-fg mt-5" on:click={() => (showAll = !showAll)}>
      <Icon name={showAll ? 'chevron-down' : 'chevron-right'} size={12} /> Everything fjord checked ({p.all.length})
    </button>
    {#if showAll}
      <div class="mt-2 border border-fjord-border rounded-lg divide-y divide-fjord-border">
        {#each p.all as c (c.id)}
          <div class="flex items-center gap-2 px-3 py-1.5 text-xs">
            <Icon name={c.status === 'ok' ? 'check' : 'alert'} size={12} class="shrink-0 {c.status === 'ok' ? 'text-fjord-success' : c.optional ? 'text-fjord-fg-dim' : 'text-fjord-warning'}" />
            <span class="text-fjord-fg-body">{c.name}</span>
            <span class="text-fjord-fg-dim truncate">{c.detail || ''}</span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
{/if}
