<script lang="ts">
  import { afterUpdate } from 'svelte';
  import Icon from './Icon.svelte';
  import { css, isBlockArt, parseAnsi, plain, type Run } from './ansi';

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

  const MAX = 3000; // cap rendered lines so huge logs stay responsive

  type Line = { runs: Run[]; text: string };
  $: parsed = parseAnsi(text).map((runs): Line => ({ runs, text: plain(runs) }));
  $: lines = parsed.map((l) => l.text);

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

  $: shownLines = (matcher && doFilter ? parsed.filter((l) => hit(l.text)) : parsed).slice(-MAX);
  $: shown = shownLines.map((l) => l.text);
  $: matchCount = matcher ? lines.filter(hit).length : 0;

  type Seg = { t: string; m: boolean; st: string };

  // Split a line into segments by colour run and by match. The regex and
  // toggle come in as PARAMETERS so the reactive `rows` statement below names
  // them as dependencies -- called bare from the template, the compiler can't
  // see inside the function, and rows whose text didn't change keep stale
  // highlighting (e.g. a query most lines match re-renders almost nothing).
  function segs(line: Line, re: RegExp | null, hl: boolean): Seg[] {
    // Match ranges on the plain text, then cut the colour runs at them.
    const hits: [number, number][] = [];
    if (re && hl) {
      re.lastIndex = 0;
      let m: RegExpExecArray | null;
      while ((m = re.exec(line.text)) !== null) {
        hits.push([m.index, m.index + m[0].length]);
        if (m[0].length === 0) re.lastIndex++; // guard against zero-width loops
      }
    }
    const out: Seg[] = [];
    let pos = 0;
    for (const r of line.runs) {
      const st = css(r.s);
      const end = pos + r.t.length;
      let at = pos;
      for (const [a, b] of hits) {
        if (b <= at || a >= end) continue;
        if (a > at) out.push({ t: line.text.slice(at, a), m: false, st });
        const stop = Math.min(b, end);
        out.push({ t: line.text.slice(Math.max(a, at), stop), m: true, st });
        at = stop;
      }
      if (at < end) out.push({ t: line.text.slice(at, end), m: false, st });
      pos = end;
    }
    return out;
  }

  // One entry per rendered line: its segments, recomputed whenever the
  // matcher or the highlight toggle changes (not just the line text).
  $: rows = shownLines.map((l) => ({ segs: segs(l, matcher, doHighlight), art: isBlockArt(l.text) }));

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
        <!-- min-h keeps an empty line; block art gets the font's own line
             height so the half-block rows meet with no gaps -->
        <div class="line whitespace-pre-wrap break-all" class:art={row.art}>{#each row.segs as s}{#if s.m}<mark class="bg-fjord-warning/40 text-fjord-fg-strong rounded-[2px] px-px">{s.t}</mark>{:else if s.st}<span style={s.st}>{s.t}</span>{:else}{s.t}{/if}{/each}</div>
      {/each}
    {/if}
  </div>
</div>

<style>
  .line {
    min-height: 1.45em;
  }
  .line.art {
    line-height: normal;
    min-height: 0;
    white-space: pre;
  }
  .chip {
    padding: 1px 7px;
    border-radius: 6px;
    color: var(--color-fjord-fg-muted);
    border: 1px solid var(--color-fjord-border);
    white-space: nowrap;
    flex-shrink: 0;
  }
  .chip:hover {
    color: var(--color-fjord-fg);
  }
  .chip.on {
    background: color-mix(in srgb, var(--color-fjord-accent) 20%, transparent);
    color: var(--color-fjord-accent-hover);
    border-color: color-mix(in srgb, var(--color-fjord-accent) 50%, transparent);
  }
</style>
