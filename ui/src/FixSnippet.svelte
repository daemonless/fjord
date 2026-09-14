<script lang="ts">
  // A readiness-check fix rendered as a shell snippet: one command per line
  // behind a "$" prompt, "#" lines as dim comments, one Copy button for the
  // whole thing. Shared by the setup wizard and the System page so a fix looks
  // and pastes the same everywhere.
  import Icon from './Icon.svelte';
  import { toast } from './toast';
  import { copyText } from './clipboard';

  export let fix = '';
  $: lines = fix.split('\n').filter((l) => l.trim() !== '');

  async function copy() {
    if (await copyText(fix.trim() + '\n')) toast('Copied', { kind: 'success', timeout: 1500 });
    else toast('Could not copy — select the text instead', { kind: 'error' });
  }
</script>

<div class="flex items-start gap-2">
  <pre class="flex-1 min-w-0 overflow-x-auto bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-xs font-mono leading-5 text-fjord-fg-secondary select-all">{#each lines as l}{#if l.trimStart().startsWith('#')}<span class="text-fjord-fg-dim">{l}</span>{:else}<span class="text-fjord-fg-dim select-none">$ </span>{l}{/if}
{/each}</pre>
  <button on:click={copy} title="Copy" class="shrink-0 flex items-center justify-center w-7 h-7 mt-0.5 rounded-md text-fjord-fg-muted hover:text-fjord-fg hover:bg-fjord-border transition-colors"><Icon name="copy" size={14} /></button>
</div>
