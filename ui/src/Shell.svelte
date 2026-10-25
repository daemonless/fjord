<script lang="ts">
  import { checkSession } from './auth';
  import InstallCheckButton from './InstallCheckButton.svelte';
  import { onMount, onDestroy } from 'svelte';
  import { onThemeChange } from './theme';
  import { xtermTheme } from './termTheme';
  import { Terminal } from '@xterm/xterm';
  import { FitAddon } from '@xterm/addon-fit';
  import '@xterm/xterm/css/xterm.css';

  export let stack: string;
  export let container: string; // container to exec into

  let el: HTMLDivElement;
  let term: Terminal;
  let fit: FitAddon;
  let ws: WebSocket | null = null;
  let ro: ResizeObserver;
  let unwatchTheme: () => void;
  let state: 'connecting' | 'connected' | 'closed' | 'error' | 'stale' = 'connecting';
  // 4002: podman's API service predates podman; the reason is the doctor's
  // detail. The panel offers the restart and tries the shell again after.
  let staleDetail = '';
  function retry() {
    staleDetail = '';
    attempts = 0;
    state = 'connecting';
    term?.reset();
    connect();
  }

  function sendResize() {
    if (ws?.readyState === WebSocket.OPEN && term) {
      ws.send('1' + JSON.stringify({ Cols: term.cols, Rows: term.rows }));
    }
  }

  let destroyed = false;
  let attempts = 0;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  const MAX_ATTEMPTS = 8;

  function connect() {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    const url = `${proto}://${location.host}/api/stacks/${encodeURIComponent(stack)}/exec?container=${encodeURIComponent(container)}`;
    ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
    // A refused upgrade (401: the session ended) looks like any close.
    ws.addEventListener('close', () => checkSession());
    ws.onopen = () => {
      state = 'connected';
      // Do NOT reset the backoff here: an exec that fails right after the
      // socket opens would otherwise reconnect forever. It resets below, on
      // the first bytes from a shell that actually runs.
      sendResize();
      term.focus();
    };
    // Flow control: xterm renders far slower than the socket can deliver
    // (a runaway `cat` streams tens of MB/s). Unbounded term.write queuing
    // balloons the tab until the browser kills the WebSocket -- so cap the
    // pending queue and drop overflow with an honest notice. A terminal is a
    // lossy display; dropping render-backlog keeps the SESSION alive.
    let pending = 0;
    let dropped = 0;
    const MAX_PENDING = 2 * 1024 * 1024;
    ws.onmessage = (e) => {
      // A shell that talks is a healthy one. Text frames are fjordd's own
      // ("exec failed: ..."), not the shell's: resetting on those retried a
      // shell that could not start forever.
      if (typeof e.data !== 'string') attempts = 0;
      const data = typeof e.data === 'string' ? e.data : new Uint8Array(e.data);
      const size = typeof data === 'string' ? data.length : data.byteLength;
      if (pending > MAX_PENDING) {
        dropped += size;
        return;
      }
      pending += size;
      term.write(data, () => {
        pending -= size;
        if (dropped > 0 && pending < 65536) {
          term.write(`\r\n\x1b[33m[fjord: display couldn't keep up -- ${(dropped / 1048576).toFixed(1)} MB of output not rendered]\x1b[0m\r\n`);
          dropped = 0;
        }
      });
    };
    ws.onclose = (e) => {
      if (destroyed) return;
      // 4001: fjordd says the image has no shell; a fresh one would fail
      // the same way. The runtime's own line is noise here: one sentence.
      if (e.code === 4001) {
        state = 'closed';
        term?.reset();
        term?.write('This image has no shell, so there is nothing to open here.\r\n');
        return;
      }
      if (e.code === 4002) {
        state = 'stale';
        staleDetail = e.reason || '';
        term?.reset();
        return;
      }
      // The exec session drops fairly often on FreeBSD; auto-reconnect (a fresh
      // shell) instead of leaving a dead terminal. Backoff, capped.
      if (attempts < MAX_ATTEMPTS) {
        attempts++;
        state = 'connecting';
        term?.write(`\r\n\x1b[90m[reconnecting… (${attempts})]\x1b[0m\r\n`);
        reconnectTimer = setTimeout(connect, Math.min(500 * attempts, 3000));
      } else {
        state = 'closed';
        term?.write('\r\n\x1b[90m[disconnected — reopen the Shell tab to retry]\x1b[0m\r\n');
      }
    };
    ws.onerror = () => {
      state = 'error';
    };
  }

  onMount(() => {
    term = new Terminal({
      fontSize: 12,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      cursorBlink: true,
      scrollback: 5000,
      // No theme meant xterm's own: pure black, whatever the page was.
      theme: xtermTheme(el.parentElement!),
    });
    unwatchTheme = onThemeChange(() => {
      if (term) term.options.theme = xtermTheme(el.parentElement!);
    });
    fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    try {
      fit.fit();
    } catch {}
    term.onData((d) => {
      if (ws?.readyState === WebSocket.OPEN) ws.send('0' + d);
    });
    ro = new ResizeObserver(() => {
      try {
        fit.fit();
        sendResize();
      } catch {}
    });
    ro.observe(el);
    connect();
  });

  onDestroy(() => {
    unwatchTheme?.();
    destroyed = true;
    if (reconnectTimer) clearTimeout(reconnectTimer);
    ro?.disconnect();
    ws?.close();
    term?.dispose();
  });
</script>

<div class="flex-1 flex flex-col min-h-0 bg-fjord-card border border-fjord-border rounded-xl overflow-hidden">
  <div class="flex items-center gap-2 px-3 py-1 border-b border-fjord-border text-[11px] text-fjord-fg-dim">
    <span class="font-mono text-fjord-fg-muted">{container}</span>
    <span class="flex-1"></span>
    {#if state === 'connected'}
      <span class="text-fjord-success">● connected</span>
    {:else if state === 'connecting'}
      <span>connecting…</span>
    {:else if state === 'error'}
      <span class="text-fjord-danger">● connection error</span>
    {:else if state === 'stale'}
      <span class="text-fjord-warning">● podman's service needs a restart</span>
    {:else}
      <span class="text-fjord-fg-dim">● closed</span>
    {/if}
  </div>
  <!-- Padded around, not on, xterm's parent: fit counts that padding as room
       for rows and the bottom line was cut off. -->
  <div class="flex-1 min-h-0 relative">
    <div bind:this={el} class="absolute inset-1.5"></div>
    {#if state === 'stale'}
      <div class="absolute inset-0 flex items-center justify-center p-6 bg-fjord-card/95">
        <div class="max-w-md flex flex-col gap-3">
          <div class="text-sm font-semibold text-fjord-fg">podman's API service is older than podman</div>
          <p class="text-sm text-fjord-fg-secondary">podman was updated under a service that kept running, and that service cannot open a shell any more. Everything else keeps working. Restarting the service fixes it and leaves the containers alone.</p>
          {#if staleDetail}<p class="text-xs text-fjord-fg-dim font-mono">{staleDetail}</p>{/if}
          <div class="flex items-center gap-3 flex-wrap">
            <InstallCheckButton id="podman-stale" name="podman's API service" label="Restart it" busyLabel="Restarting…" on:installed={retry} />
            <span class="text-xs text-fjord-fg-dim">or, as root: <code class="font-mono">service podman_service restart</code></span>
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>
