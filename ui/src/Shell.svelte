<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
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
  let state: 'connecting' | 'connected' | 'closed' | 'error' = 'connecting';

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
      attempts = 0; // a shell that talks is a healthy one
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
    ws.onclose = () => {
      if (destroyed) return;
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
    {:else}
      <span class="text-fjord-fg-dim">● closed</span>
    {/if}
  </div>
  <div bind:this={el} class="flex-1 min-h-0 p-1.5"></div>
</div>
