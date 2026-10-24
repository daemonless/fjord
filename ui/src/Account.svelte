<script lang="ts">
  import { onMount } from 'svelte';
  import Spinner from './Spinner.svelte';
  import { auth, loadAuth, postJSON } from './auth';
  import { toast } from './toast';

  // Settings > Account: the user name and password, where this account is
  // signed in, and recent sign-in activity.
  type Session = { id: string; browser: string; addr: string; created: string; lastUsed: string; current: boolean };
  type Event = { time: string; kind: string; addr?: string; text: string };

  let loading = true;
  let error = '';
  let user = '';
  let sessions: Session[] = [];
  let activity: Event[] = [];

  async function load() {
    try {
      const r = await fetch('/api/auth/account');
      if (!r.ok) throw new Error((await r.text()).trim() || String(r.status));
      const a = await r.json();
      user = a.user;
      newUser = a.user;
      sessions = a.sessions ?? [];
      activity = a.activity ?? [];
      error = '';
    } catch (e) {
      error = String((e as Error).message ?? e);
    }
    loading = false;
  }
  onMount(load);

  // Password change: the current password first; other browsers sign out.
  let current = '';
  let newUser = '';
  let password = '';
  let again = '';
  let changing = false;
  let changeError = '';
  async function changePassword() {
    changeError = '';
    if (password !== again) {
      changeError = 'The two new passwords differ.';
      return;
    }
    changing = true;
    const r = await postJSON('/api/auth/password', { current, user: newUser, password });
    changing = false;
    if (!r.ok) {
      changeError = r.error || `fjord answered ${r.status}`;
      return;
    }
    current = password = again = '';
    toast('Password changed. Other browsers are signed out.', { kind: 'success' });
    await Promise.all([load(), loadAuth()]);
  }

  async function endSession(s: Session) {
    const r = await fetch(`/api/auth/sessions/${s.id}`, { method: 'DELETE' });
    if (!r.ok) {
      toast(`Could not sign that browser out: ${(await r.text()).trim()}`, { kind: 'error' });
      return;
    }
    await load();
  }

  let everywhereOpen = false;
  let everywherePassword = '';
  let everywhereError = '';
  async function endOthers() {
    everywhereError = '';
    const r = await postJSON('/api/auth/sessions', { current: everywherePassword });
    if (!r.ok) {
      everywhereError = r.error || `fjord answered ${r.status}`;
      return;
    }
    everywhereOpen = false;
    everywherePassword = '';
    toast('Every other browser is signed out.', { kind: 'success' });
    await load();
  }

  // "Firefox on Linux" from a User-Agent; the raw one stays in the tooltip.
  function browserName(ua: string): string {
    const b = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : /curl\//.test(ua) ? 'curl' : '';
    const os = /iPhone|iPad/.test(ua) ? 'iOS' : /Android/.test(ua) ? 'Android' : /Mac OS X/.test(ua) ? 'macOS' : /Windows/.test(ua) ? 'Windows' : /FreeBSD/.test(ua) ? 'FreeBSD' : /Linux/.test(ua) ? 'Linux' : '';
    if (b && os) return `${b} on ${os}`;
    return b || os || 'Unknown browser';
  }

  function when(iso: string): string {
    const d = new Date(iso);
    const mins = Math.round((Date.now() - d.getTime()) / 60000);
    if (mins < 2) return 'just now';
    if (mins < 60) return `${mins} minutes ago`;
    if (mins < 48 * 60) return `${Math.round(mins / 60)} hours ago`;
    return d.toLocaleDateString();
  }

  const input =
    'w-full bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-sm text-fjord-fg-body focus:outline-none focus:border-fjord-accent';
</script>

<div class="max-w-2xl">
  {#if $auth && $auth !== 'unreachable' && $auth.off}
    <p class="text-sm text-fjord-warning">
      Sign-in is off (<code class="font-mono">FJORD_AUTH=none</code>): only this host and the proxy in front of it can reach fjord, with no
      password.
    </p>
  {:else if loading}
    <div class="flex items-center gap-3 text-fjord-fg-dim text-sm"><Spinner size={18} /> Loading…</div>
  {:else if error}
    <p class="text-sm text-fjord-danger">{error}</p>
  {:else}
    <h3 class="text-sm font-semibold text-fjord-fg-secondary mb-1">Signed in</h3>
    <p class="text-xs text-fjord-fg-dim mb-3">Browsers signed in as <b class="text-fjord-fg-body">{user}</b>. A session ends after 30 days unused.</p>
    <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border">
      {#each sessions as s (s.id)}
        <div class="flex items-center gap-4 px-4 py-3">
          <div class="min-w-0 flex-1">
            <div class="text-sm text-fjord-fg-body" title={s.browser}>
              {browserName(s.browser)}
              {#if s.current}<span class="ml-2 text-[10px] font-semibold uppercase tracking-wide text-fjord-success">this browser</span>{/if}
            </div>
            <div class="text-xs text-fjord-fg-dim font-mono">{s.addr} · last used {when(s.lastUsed)}</div>
          </div>
          {#if !s.current}
            <button on:click={() => endSession(s)} class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-border hover:bg-fjord-danger hover:text-white transition-colors">Sign out</button>
          {/if}
        </div>
      {/each}
    </div>
    {#if sessions.length > 1}
      {#if everywhereOpen}
        <form on:submit|preventDefault={endOthers} class="flex items-center gap-2 mt-3">
          <input type="password" bind:value={everywherePassword} placeholder="Your password" aria-label="Your password" autocomplete="current-password" class="{input} max-w-60" required />
          <button type="submit" class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-danger hover:bg-fjord-danger-hover text-white">Sign out everywhere else</button>
          <button type="button" on:click={() => (everywhereOpen = false)} class="shrink-0 px-3 py-1.5 text-sm text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
        </form>
        {#if everywhereError}<p class="text-sm text-fjord-danger mt-2" role="alert">{everywhereError}</p>{/if}
      {:else}
        <button on:click={() => (everywhereOpen = true)} class="mt-3 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-border hover:bg-fjord-danger hover:text-white transition-colors">Sign out everywhere else…</button>
      {/if}
    {/if}

    <h3 class="text-sm font-semibold text-fjord-fg-secondary mt-8 mb-1">User name and password</h3>
    <p class="text-xs text-fjord-fg-dim mb-3">Every other browser is signed out when you change them. Forgot the password? On the host, as root: <code class="font-mono">fjordd passwd</code></p>
    <form on:submit|preventDefault={changePassword} class="grid grid-cols-[10rem_1fr] gap-x-4 gap-y-3 items-center">
      <label class="text-sm text-fjord-fg-muted" for="acct-current">Current password</label>
      <input id="acct-current" type="password" bind:value={current} autocomplete="current-password" class={input} required />
      <label class="text-sm text-fjord-fg-muted" for="acct-user">User name</label>
      <input id="acct-user" bind:value={newUser} autocomplete="username" class={input} required />
      <label class="text-sm text-fjord-fg-muted" for="acct-new">New password</label>
      <input id="acct-new" type="password" bind:value={password} autocomplete="new-password" minlength="12" placeholder="At least 12 characters" class={input} required />
      <label class="text-sm text-fjord-fg-muted" for="acct-again">New password again</label>
      <input id="acct-again" type="password" bind:value={again} autocomplete="new-password" class={input} required />
      <div></div>
      <div class="flex items-center gap-3">
        <button type="submit" disabled={changing} class="bg-fjord-accent hover:bg-fjord-accent-hover text-white text-sm font-medium py-1.5 px-4 rounded-lg disabled:opacity-50">{changing ? 'Changing…' : 'Change password'}</button>
        {#if changeError}<span class="text-sm text-fjord-danger" role="alert">{changeError}</span>{/if}
      </div>
    </form>

    <h3 class="text-sm font-semibold text-fjord-fg-secondary mt-8 mb-1">Recent sign-in activity</h3>
    {#if activity.length}
      <div class="border border-fjord-border rounded-xl overflow-hidden divide-y divide-fjord-border">
        {#each activity as e}
          <div class="flex items-center gap-4 px-4 py-2.5">
            <span class="w-32 shrink-0 text-xs text-fjord-fg-dim" title={new Date(e.time).toLocaleString()}>{when(e.time)}</span>
            <span class="min-w-0 flex-1 text-sm {e.kind === 'failed' ? 'text-fjord-warning' : 'text-fjord-fg-body'}">{e.text}</span>
            {#if e.addr}<span class="shrink-0 text-xs text-fjord-fg-dim font-mono">{e.addr}</span>{/if}
          </div>
        {/each}
      </div>
    {:else}
      <p class="text-sm text-fjord-fg-dim">Nothing yet: failed sign-ins, password changes and sign-ins from new addresses show here.</p>
    {/if}
  {/if}
</div>
