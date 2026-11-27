<script lang="ts">
  // podman's network backend on this host, and switching it both ways
  // (podman 5 on FreeBSD, through netavark-migrate). The switch runs in
  // fjordd: a page opened while it runs shows its output so far.
  import { onMount, onDestroy } from 'svelte';

  type State = {
    backend: string;
    podman: number;
    tool: boolean;
    canSwitch: boolean;
    canBack: boolean;
    note?: string;
    stacks: string[] | null;
    networks: string[] | null;
    running: boolean;
    to?: string;
    result?: string;
    log?: string;
  };

  let st: State | null = null;
  let confirming = false;
  let output = '';
  let streaming = false;
  let poll: ReturnType<typeof setInterval> | null = null;

  const label = (b: string) => (b === 'cni' ? 'CNI' : b);

  async function load() {
    try {
      const r = await fetch('/api/system/netavark');
      if (!r.ok) return;
      st = await r.json();
      if (st && !streaming) output = st.log ?? '';
      // Running without this page's stream (another tab, a reload): follow it.
      if (st?.running && !streaming && !poll) poll = setInterval(load, 3000);
      if (!st?.running && poll) {
        clearInterval(poll);
        poll = null;
      }
    } catch {}
  }

  async function run() {
    if (!st) return;
    const to = st.canSwitch ? 'netavark' : 'cni';
    confirming = false;
    streaming = true;
    output = '';
    try {
      const r = await fetch('/api/system/netavark/switch?stream=1', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ to }),
      });
      if (!r.ok) {
        output = (await r.text()).trim();
        return;
      }
      const reader = r.body?.getReader();
      const decoder = new TextDecoder('utf-8');
      while (reader) {
        const { done, value } = await reader.read();
        if (done) break;
        output += decoder.decode(value, { stream: true });
      }
    } catch (e: any) {
      output += `\n${e?.message || e}`;
    } finally {
      streaming = false;
      load();
    }
  }

  $: stacks = st?.stacks ?? [];
  $: networks = st?.networks ?? [];
  $: busy = streaming || !!st?.running;
  $: target = st?.canSwitch ? 'netavark' : 'CNI';

  onMount(load);
  onDestroy(() => poll && clearInterval(poll));
</script>

{#if st && (st.backend || st.note)}
  <section data-network-backend>
    <h3 class="text-lg font-bold text-fjord-fg mt-8 mb-1">Network backend</h3>
    <p class="text-sm text-fjord-fg-dim mb-3 max-w-prose">
      {#if st.backend}podman connects containers with its <b class="text-fjord-fg-body">{label(st.backend)}</b> network backend.{/if}
      {#if st.note}{st.note}{/if}
    </p>

    {#if (st.canSwitch || st.canBack) && !busy}
      {#if !confirming}
        <button
          on:click={() => (confirming = true)}
          data-switch-backend
          class="px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-border hover:bg-fjord-border/70 text-fjord-fg-body transition-colors"
          >{st.canSwitch ? 'Switch to netavark…' : 'Switch back to CNI…'}</button
        >
      {:else}
        <div class="max-w-2xl rounded-lg border border-fjord-warning/30 bg-fjord-warning/10 px-4 py-3 text-sm text-fjord-fg-body" data-switch-confirm>
          <p class="font-medium mb-1">Switch podman to {target}?</p>
          <p class="mb-1">
            {#if stacks.length}
              These stop while podman moves and start again after: <span class="font-mono text-xs">{stacks.join(', ')}</span>.
            {:else}
              No stack on podman is running, so nothing stops.
            {/if}
          </p>
          {#if networks.length}
            <p class="mb-1">These networks are made again on {target}, with the same names and addresses: <span class="font-mono text-xs">{networks.join(', ')}</span>.</p>
          {/if}
          <p class="text-fjord-fg-dim mb-3">AppJail is not touched. {st.canSwitch ? 'You can switch back to CNI from here later.' : ''}</p>
          <div class="flex gap-2">
            <button on:click={() => (confirming = false)} class="px-3 py-1.5 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
            <button on:click={run} data-switch-go class="px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-accent hover:bg-fjord-accent-hover text-white"
              >Switch to {target}</button
            >
          </div>
        </div>
      {/if}
    {/if}

    {#if busy || output}
      <div class="mt-3 max-w-3xl">
        <div class="text-xs text-fjord-fg-dim mb-1">
          {#if busy}Switching to {label(st.to || (st.canSwitch ? 'netavark' : 'cni'))}… it carries on if you leave this page.
          {:else if st.result === 'ok'}Done.
          {:else if st.result === 'refused'}Nothing was changed.
          {:else if st.result === 'failed'}Not everything came back: the lines below say what.{/if}
        </div>
        <pre class="bg-fjord-inset border border-fjord-border rounded-lg p-3 text-xs text-fjord-fg-secondary whitespace-pre-wrap max-h-80 overflow-y-auto" data-switch-log>{output}</pre>
      </div>
    {/if}
  </section>
{/if}
