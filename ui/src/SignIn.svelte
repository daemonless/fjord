<script lang="ts">
  import { onMount, tick } from 'svelte';
  import Icon from './Icon.svelte';
  import { loadAuth, postJSON, type AuthState } from './auth';

  // Set up (no account yet: the setup code from the host) or Sign in. As an
  // overlay it sits over an open page whose session ended.
  export let state: AuthState;
  export let overlay = false;
  export let onSignedIn: () => void = () => {};

  $: setup = !state.account;
  let code = '';
  let user = state.user || 'admin';
  let password = '';
  let again = '';
  let busy = false;
  let error = '';
  let first: HTMLInputElement;

  onMount(async () => {
    await tick();
    first?.focus();
  });

  async function submit() {
    error = '';
    if (setup && password !== again) {
      error = 'The two passwords differ.';
      return;
    }
    busy = true;
    const r = setup
      ? await postJSON('/api/auth/setup', { code, user, password })
      : await postJSON('/api/auth/login', { user, password });
    busy = false;
    if (!r.ok) {
      error = r.error || `fjord answered ${r.status}`;
      password = '';
      again = '';
      return;
    }
    await loadAuth();
    onSignedIn();
  }

  const input =
    'w-full bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-sm text-fjord-fg-body focus:outline-none focus:border-fjord-accent';
</script>

<div class="{overlay ? 'fixed inset-0 z-50 bg-black/50' : 'min-h-screen bg-fjord-bg'} flex items-center justify-center px-4">
  <form
    on:submit|preventDefault={submit}
    class="w-full max-w-sm bg-fjord-card border border-fjord-border rounded-xl p-6 shadow-lg"
    aria-labelledby="signin-title"
  >
    <div class="flex items-center gap-3 mb-5">
      <div class="w-10 h-10 rounded-xl bg-fjord-accent/15 border border-fjord-accent/30 flex items-center justify-center text-fjord-accent">
        <Icon name="mountain" size={22} />
      </div>
      <div>
        <h1 id="signin-title" class="text-lg font-bold text-fjord-fg">{setup ? 'Set up fjord' : overlay ? 'Sign in again' : 'Sign in to fjord'}</h1>
        <p class="text-sm text-fjord-fg-dim">
          {#if setup}Choose the account that manages this host.{:else if overlay}Your session ended. What you had open is kept.{:else}{location.host}{/if}
        </p>
      </div>
    </div>

    {#if setup}
      <label class="block text-sm text-fjord-fg-secondary mb-1" for="signin-code">Setup code</label>
      <input id="signin-code" bind:this={first} bind:value={code} class="{input} font-mono uppercase" autocomplete="off" spellcheck="false" required />
      <p class="text-xs text-fjord-fg-dim mt-1">On the host, as root:</p>
      <code class="block mb-4 mt-0.5 text-xs font-mono text-fjord-fg-body select-all break-all">cat {state.setupCodePath}</code>
    {/if}

    <label class="block text-sm text-fjord-fg-secondary mb-1" for="signin-user">User name</label>
    {#if setup}
      <input id="signin-user" bind:value={user} class="{input} mb-4" autocomplete="username" required />
    {:else}
      <input id="signin-user" bind:value={user} bind:this={first} class="{input} mb-4" autocomplete="username" required />
    {/if}

    <label class="block text-sm text-fjord-fg-secondary mb-1" for="signin-password">Password</label>
    {#if setup}
      <input id="signin-password" type="password" bind:value={password} class="{input} mb-1" autocomplete="new-password" minlength="12" required />
      <p class="text-xs text-fjord-fg-dim mb-4">At least 12 characters.</p>
      <label class="block text-sm text-fjord-fg-secondary mb-1" for="signin-again">Password again</label>
      <input id="signin-again" type="password" bind:value={again} class="{input} mb-4" autocomplete="new-password" required />
    {:else}
      <input id="signin-password" type="password" bind:value={password} class="{input} mb-4" autocomplete="current-password" required />
    {/if}

    {#if error}
      <p class="text-sm text-fjord-danger mb-4" role="alert">{error}</p>
    {/if}

    <button
      type="submit"
      disabled={busy}
      class="w-full bg-fjord-accent hover:bg-fjord-accent-hover text-white text-sm font-medium py-2 px-4 rounded-lg disabled:opacity-50"
      >{busy ? (setup ? 'Setting up…' : 'Signing in…') : setup ? 'Set up' : 'Sign in'}</button
    >
    {#if !setup}
      <p class="text-xs text-fjord-fg-dim mt-4">Forgot it? On the host, as root: <code class="font-mono">fjordd passwd</code></p>
    {/if}
  </form>
</div>
