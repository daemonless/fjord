<script lang="ts">
  import { afterUpdate } from 'svelte';
  import Icon from './Icon.svelte';

  export let text = ''; // the raw log buffer (may contain ANSI + grow live)
  export let streaming = false;

  let query = '';
  let caseSensitive = false;
  let useRegex = false;
  let doFilter = true; // limit to matching lines
  let doHighlight = true; // mark matches
  let copied = false;

  let scroller: HTMLDivElement;
  let searchInput: HTMLInputElement;
  let stick = true; // pinned to bottom (live tail)

  const ANSI = /\x1b\[[0-9;]*m/g;
  const MAX = 3000; // cap rendered lines so huge logs stay responsive

  $: lines = text.replace(ANSI, '').split('\n');

  // One matcher, rebuilt when the query/options change. Invalid regex -> null
  // (treated as "no filter") so a half-typed pattern doesn't blow up.
  $: matcher = (() => {
    if (!query) return null;
    const src = useRegex ? query : query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    try {
      return new RegExp(src, caseSensitive ? 'g' : 'gi');
    } catch {
      return null;
    }
  })();

  function hit(line: string): boolean {
    if (!matcher) return true;
    matcher.lastIndex = 0;
    return matcher.test(line);
  }

  $: shown = (matcher && doFilter ? lines.filter(hit) : lines).slice(-MAX);
  $: matchCount = matcher ? lines.filter(hit).length : 0;

  // Split a line into highlighted / plain segments. The regex and toggle come
  // in as PARAMETERS so the reactive `rows` statement below names them as
  // dependencies -- called bare from the template, the compiler can't see
  // inside the function, and rows whose text didn't change keep stale
  // highlighting (e.g. a query most lines match re-renders almost nothing).
  function segs(line: string, re: RegExp | null, hl: boolean): { t: string; m: boolean }[] {
    if (!re || !hl) return [{ t: line, m: false }];
    const out: { t: string; m: boolean }[] = [];
    let last = 0;
    re.lastIndex = 0;
    let m: RegExpExecArray | null;
    while ((m = re.exec(line)) !== null) {
      if (m.index > last) out.push({ t: line.slice(last, m.index), m: false });
      out.push({ t: m[0], m: true });
      last = m.index + m[0].length;
      if (m[0].length === 0) re.lastIndex++; // guard against zero-width loops
    }
    if (last < line.length) out.push({ t: line.slice(last), m: false });
    return out;
  }

  // One entry per rendered line: its highlight segments, recomputed whenever
  // the matcher or the highlight toggle changes (not just the line text).
  $: rows = shown.map((line) => segs(line, matcher, doHighlight));

  function onScroll() {
    if (!scroller) return;
    stick = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 30;
  }
  afterUpdate(() => {
    if (stick && scroller) scroller.scrollTop = scroller.scrollHeight;
  });

  function onKey(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && (e.key === 'f' || e.key === 'F')) {
      e.preventDefault();
      searchInput?.focus();
      searchInput?.select();
    }
  }

  async function copy() {
    const out = shown.join('\n');
    try {
      if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(out);
      else {
        const ta = document.createElement('textarea');
        ta.value = out;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      }
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {}
  }
</script>

<svelte:window on:keydown={onKey} />

<div class="flex-1 flex flex-col min-h-0 bg-fjord-card border border-fjord-border rounded-xl overflow-hidden">
  <!-- search / options bar -->
  <div class="flex items-center gap-2 px-3 py-1.5 border-b border-fjord-border text-xs">
    <span class="text-fjord-fg-dim shrink-0"><Icon name="search" size={13} /></span>
    <input
      bind:this={searchInput}
      bind:value={query}
      placeholder="Find in logs…  (Ctrl+F)"
      class="flex-1 min-w-0 bg-transparent text-fjord-fg-body outline-none placeholder:text-fjord-fg-faint"
    />
    {#if query}
      <span class="text-fjord-fg-dim shrink-0 tabular-nums">{matchCount} match{matchCount === 1 ? '' : 'es'}</span>
    {/if}
    <button class="chip" class:on={doFilter} on:click={() => (doFilter = !doFilter)} title="Show only matching lines">Filter</button>
    <button class="chip" class:on={doHighlight} on:click={() => (doHighlight = !doHighlight)} title="Highlight matches">Highlight</button>
    <span class="w-px h-4 bg-fjord-border"></span>
    <button class="chip" class:on={caseSensitive} on:click={() => (caseSensitive = !caseSensitive)} title="Case sensitive">Aa</button>
    <button class="chip font-mono" class:on={useRegex} on:click={() => (useRegex = !useRegex)} title="Regular expression">.*</button>
    <span class="w-px h-4 bg-fjord-border"></span>
    <button class="chip" on:click={copy} title="Copy shown lines"><Icon name={copied ? 'check' : 'copy'} size={12} /></button>
    {#if streaming}<span class="w-1.5 h-1.5 rounded-full bg-fjord-warning animate-pulse shrink-0" title="Live"></span>{/if}
  </div>

  <!-- lines -->
  <div
    bind:this={scroller}
    on:scroll={onScroll}
    class="flex-1 overflow-auto font-mono text-[11.5px] leading-[1.45] p-2 text-fjord-fg-secondary"
  >
    {#if shown.length === 0 || (shown.length === 1 && shown[0] === '')}
      <div class="text-fjord-fg-faint italic px-1">{query ? 'No matching lines.' : 'No output yet…'}</div>
    {:else}
      {#each rows as row}
        <div class="whitespace-pre-wrap break-all">{#each row as s}{#if s.m}<mark class="bg-fjord-warning/40 text-fjord-fg-strong rounded-[2px] px-px">{s.t}</mark>{:else}{s.t}{/if}{/each}</div>
      {/each}
    {/if}
  </div>
</div>

<style>
  .chip {
    padding: 1px 7px;
    border-radius: 6px;
    color: #94a3b8;
    border: 1px solid var(--color-fjord-border);
    white-space: nowrap;
    flex-shrink: 0;
  }
  .chip:hover {
    color: #fff;
  }
  .chip.on {
    background: color-mix(in srgb, var(--color-fjord-accent) 20%, transparent);
    color: var(--color-fjord-accent-hover);
    border-color: color-mix(in srgb, var(--color-fjord-accent) 50%, transparent);
  }
</style>
