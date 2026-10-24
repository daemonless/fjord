<script lang="ts">
  import { onMount } from 'svelte';
  import App from './App.svelte';
  import SignIn from './SignIn.svelte';
  import Spinner from './Spinner.svelte';
  import { auth, loadAuth, SIGNED_IN } from './auth';

  // The sign-in boundary: App mounts only once signed in. A session that
  // ends later (a 401) puts Sign in over the open page, so unsaved edits
  // survive; signing in again tells the page to reload what failed.
  let started = false;
  $: if ($auth && $auth !== 'unreachable' && $auth.signedIn) started = true;

  onMount(loadAuth);
</script>

{#if $auth === null}
  <div class="min-h-screen bg-fjord-bg flex items-center justify-center"><Spinner size={24} /></div>
{:else if $auth === 'unreachable' && !started}
  <div class="min-h-screen bg-fjord-bg flex flex-col items-center justify-center gap-3 px-4 text-center">
    <p class="text-fjord-fg">fjord is not answering.</p>
    <button class="bg-fjord-accent hover:bg-fjord-accent-hover text-white text-sm font-medium py-1.5 px-4 rounded-lg" on:click={loadAuth}>Try again</button>
  </div>
{:else}
  {#if started}
    <App />
  {/if}
  {#if $auth !== 'unreachable' && !$auth.signedIn}
    <SignIn state={$auth} overlay={started} onSignedIn={() => started && window.dispatchEvent(new Event(SIGNED_IN))} />
  {/if}
{/if}
