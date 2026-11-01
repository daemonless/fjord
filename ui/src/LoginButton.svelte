<script lang="ts">
  import { fitMenu } from './fitMenu';
  // How to sign in to the stack's app, beside Open: the login its catalog
  // app declares, with the password fjord made up at install, or the app's
  // own default and a word to change it. Nothing when the app declares none.
  import Icon from './Icon.svelte';
  import { copyText } from './clipboard';
  import { toast } from './toast';

  export let stack: string;

  type Login = { user?: string; password?: string; default?: boolean; note?: string };
  let login: Login | null = null;
  let open = false;
  let shown = false;

  async function load(name: string) {
    login = null;
    open = shown = false;
    try {
      const r = await fetch(`/api/stacks/${encodeURIComponent(name)}/login`);
      if (r.status === 200 && name === stack) login = await r.json();
    } catch {
      // no login card is the honest fallback
    }
  }
  $: load(stack);

  async function copy(what: string, value: string) {
    if (await copyText(value)) toast(`${what} copied`, { kind: 'success', timeout: 1500 });
  }
</script>

{#if login}
  <details class="relative shrink-0" bind:open>
    <summary
      class="list-none cursor-pointer flex items-center gap-1 text-sm font-medium text-fjord-accent hover:text-fjord-accent-hover"
      title="How to sign in to this app">Sign in</summary
    >
    <div use:fitMenu class="absolute z-30 left-0 mt-1 w-80 bg-fjord-card border border-fjord-border rounded-lg shadow-xl p-3 text-sm">
      {#if login.user}
        <div class="flex items-center gap-2">
          <span class="w-20 text-fjord-fg-muted">User</span>
          <span class="flex-1 font-mono text-fjord-fg-body truncate">{login.user}</span>
          <button on:click={() => copy('User', login?.user ?? '')} title="Copy" aria-label="Copy the user name"
            class="text-fjord-fg-dim hover:text-fjord-fg"><Icon name="copy" size={13} /></button>
        </div>
      {/if}
      {#if login.password}
        <div class="flex items-center gap-2 mt-1.5">
          <span class="w-20 text-fjord-fg-muted">Password</span>
          <button
            on:click={() => (shown = !shown)}
            title={shown ? 'Hide' : 'Show'}
            class="flex-1 text-left font-mono text-fjord-fg-body truncate">{shown ? login.password : '••••••••••'}</button
          >
          <button on:click={() => copy('Password', login?.password ?? '')} title="Copy" aria-label="Copy the password"
            class="text-fjord-fg-dim hover:text-fjord-fg"><Icon name="copy" size={13} /></button>
        </div>
      {/if}
      {#if login.default}
        <p class="mt-2 text-xs text-fjord-warning">The app's default login, the same everywhere it is installed: change it in the app.</p>
      {/if}
      {#if login.note}
        <p class="mt-2 text-xs text-fjord-fg-secondary">{login.note}</p>
      {/if}
    </div>
  </details>
{/if}
