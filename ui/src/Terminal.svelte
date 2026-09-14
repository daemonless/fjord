<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { Terminal } from '@xterm/xterm';
  import { FitAddon } from '@xterm/addon-fit';
  import '@xterm/xterm/css/xterm.css';

  export let logs = '';
  export let status: 'idle' | 'running' | 'error' = 'idle';
  export let statusMessage = '';

  let el: HTMLDivElement;
  let term: Terminal | null = null;
  let fit: FitAddon;
  let written = 0; // how much of `logs` we've already written to the terminal
  let copied = false;
  let ro: ResizeObserver;

  onMount(() => {
    term = new Terminal({
      convertEol: true, // podman emits bare \n; treat as newline
      fontSize: 12,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      scrollback: 5000,
      disableStdin: true,
      cursorInactiveStyle: 'none',
      theme: { background: '#161619', foreground: '#cbd5e1', selectionBackground: '#334155' },
    });
    fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    fit.fit();
    if (logs) {
      term.write(logs);
      written = logs.length;
    }
    ro = new ResizeObserver(() => fit.fit());
    ro.observe(el);
  });

  onDestroy(() => {
    ro?.disconnect();
    term?.dispose();
  });

  // Write only newly-appended output so \r-based progress lines (podman pull)
  // overwrite in place instead of piling up. A shorter `logs` means it was
  // cleared, so reset the emulator.
  $: if (term) {
    if (logs.length < written) {
      term.reset();
      written = 0;
    }
    if (logs.length > written) {
      term.write(logs.slice(written));
      written = logs.length;
    }
  }

  function clearLogs() {
    logs = '';
  }
  async function copyLogs() {
    if (!logs) return;
    // navigator.clipboard only exists in secure contexts (HTTPS/localhost).
    // fjord is usually served over plain http on the LAN, so fall back to the
    // legacy execCommand path via a temporary textarea.
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(logs);
      } else {
        const ta = document.createElement('textarea');
        ta.value = logs;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.focus();
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      }
      copied = true;
      setTimeout(() => (copied = false), 2000);
    } catch {
      // clipboard blocked entirely -- leave the button state unchanged
    }
  }
</script>

<div class="flex-1 bg-fjord-card border border-fjord-border rounded-xl shadow-xl flex flex-col overflow-hidden">
  <div
    class="bg-fjord-border/50 px-4 py-2.5 flex items-center justify-between border-b border-fjord-border text-xs font-semibold text-fjord-fg-secondary"
  >
    <div class="flex items-center gap-3">
      <span>Terminal</span>
      {#if status === 'running'}
        <span
          class="flex items-center gap-1.5 text-fjord-warning bg-fjord-warning/10 px-2 py-0.5 rounded text-[10px] font-mono font-normal"
        >
          <span class="w-1.5 h-1.5 rounded-full bg-fjord-warning animate-ping"></span>
          {statusMessage || 'Executing…'}
        </span>
      {:else if status === 'error'}
        <span class="text-fjord-danger bg-fjord-danger/10 px-2 py-0.5 rounded text-[10px] font-mono font-normal">
          {statusMessage || 'Error'}
        </span>
      {:else if logs}
        <span class="text-fjord-success bg-fjord-success/10 px-2 py-0.5 rounded text-[10px] font-mono font-normal">
          Done
        </span>
      {/if}
    </div>
    {#if logs}
      <div class="flex items-center gap-2">
        <button
          on:click={copyLogs}
          class="text-fjord-fg-muted hover:text-fjord-fg transition-colors px-2 py-0.5 rounded bg-fjord-border/40 hover:bg-fjord-border text-[11px] font-normal"
        >
          {copied ? 'Copied' : 'Copy'}
        </button>
        <button
          on:click={clearLogs}
          class="text-fjord-fg-muted hover:text-fjord-fg transition-colors px-2 py-0.5 rounded bg-fjord-border/40 hover:bg-fjord-border text-[11px] font-normal"
        >
          Clear
        </button>
      </div>
    {/if}
  </div>

  <div class="flex-1 bg-fjord-inset relative overflow-hidden">
    <div bind:this={el} class="absolute inset-0 p-2"></div>
    {#if !logs}
      <p class="absolute top-3 left-4 text-fjord-fg-faint text-sm font-mono italic pointer-events-none">
        Waiting for output…
      </p>
    {/if}
  </div>
</div>
