<script lang="ts">
  import { fitMenu } from './fitMenu';
  // The stack's restart policy, a chip among the header chips: what happens
  // when it exits, and at boot -- or, on an engine that does not restart what
  // exits, only what starts at boot. fjordd keeps it (the compose's
  // `restart:`, or the stack's state); running containers take a compose
  // change at the next Apply, like any saved change.
  import { createEventDispatcher } from 'svelte';
  import Icon from './Icon.svelte';
  import { toast } from './toast';
  import {
    DEFAULT_RETRIES,
    BOOT_ONLY_NOTE,
    choicesFor,
    composeValue,
    restartMeaning,
    restartShort,
    restartTitle,
    type StackRestart,
  } from './restartPolicy';

  export let name: string;
  export let restart: StackRestart | undefined;
  export let disabled = false;
  /** Why it is disabled, when it is. */
  export let reason = '';

  const dispatch = createEventDispatcher<{ changed: void }>();
  let open = false;
  let saving = false;
  let retries = DEFAULT_RETRIES;
  $: if (restart?.policy === 'on-failure' && !restart.mixed) retries = restart.retries || DEFAULT_RETRIES;
  $: onExit = !!restart?.onExit;
  // A boot-only stack from before fjord set one starts at boot when it was
  // running: that is "Start at boot".
  $: current = !restart || restart.mixed ? '' : restart.policy || (onExit ? '' : 'unless-stopped');
  $: choices = restart ? choicesFor(restart.policies, onExit) : [];
  $: off = disabled || saving || !choices.length;
  $: if (off && !saving) open = false;

  async function save(policy: string, n = retries) {
    saving = true;
    try {
      const res = await fetch(`/api/stacks/${encodeURIComponent(name)}/restart-policy`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ policy, retries: policy === 'on-failure' ? n : 0 }),
      });
      if (!res.ok) {
        toast(`Restart policy not saved: ${(await res.text()).trim()}`, { kind: 'error' });
        return;
      }
      // The count sits beside its choice: keep the menu open to set it.
      if (policy !== 'on-failure') open = false;
      dispatch('changed');
    } catch {
      toast('Restart policy not saved: fjordd did not answer', { kind: 'error' });
    } finally {
      saving = false;
    }
  }
</script>

<svelte:window on:keydown={(e) => open && e.key === 'Escape' && (open = false)} />

{#if restart}
  <div class="relative shrink-0" data-restart-policy>
    <button
      type="button"
      on:click={() => (open = !open)}
      disabled={off}
      aria-haspopup="menu"
      aria-expanded={open}
      data-policy={current}
      title={reason || restartTitle(restart)}
      class="flex items-center gap-1 bg-fjord-card border border-fjord-border rounded-full px-2.5 py-0.5 text-[11px] text-fjord-fg-muted hover:text-fjord-fg-body hover:border-fjord-fg-faint focus:outline-none focus:border-fjord-accent disabled:opacity-50 disabled:hover:text-fjord-fg-muted disabled:hover:border-fjord-border"
      ><Icon name={onExit || !choices.length ? 'restart' : 'power'} size={11} /><span>{restartShort(restart)}</span><span aria-hidden="true">▾</span></button
    >
    {#if open}
      <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
      <div class="fixed inset-0 z-20" on:click={() => (open = false)}></div>
      <div role="menu" use:fitMenu class="absolute z-30 left-0 mt-1 w-96 bg-fjord-card border border-fjord-border rounded-lg shadow-xl py-1">
        {#if restart.mixed}
          <p class="px-3 py-1.5 text-xs text-fjord-fg-dim">{restartTitle(restart)}</p>
        {/if}
        {#each choices as c}
          <div class="flex items-start gap-2 px-3 py-2 hover:bg-fjord-border/50 {current === c.value ? 'bg-fjord-border/30' : ''}">
            <button
              type="button"
              role="menuitemradio"
              aria-checked={current === c.value}
              disabled={saving}
              on:click={() => current !== c.value && save(c.value)}
              class="flex-1 flex items-start gap-2 text-left"
            >
              <span
                class="mt-1 w-3 h-3 shrink-0 rounded-full border {current === c.value
                  ? 'border-fjord-accent bg-fjord-accent shadow-[inset_0_0_0_2px_var(--color-fjord-card)]'
                  : 'border-fjord-fg-faint'}"
              ></span>
              <span class="flex flex-col flex-1">
                <span class="flex items-baseline justify-between gap-3">
                  <span class="text-sm font-medium text-fjord-fg-body">{c.label}</span>
                  {#if onExit}<code class="text-[11px] text-fjord-fg-muted">{composeValue(c.value, c.value === 'on-failure' ? retries : 0)}</code>{/if}
                </span>
                <span class="text-xs text-fjord-fg-dim">{restartMeaning(c.value, c.value === 'on-failure' ? retries : 0, onExit)}</span>
              </span>
            </button>
            {#if c.value === 'on-failure' && current === 'on-failure'}
              <input
                type="number"
                aria-label="Restarts after a crash"
                min="1"
                max="100"
                bind:value={retries}
                disabled={saving}
                on:change={() => save('on-failure', Math.max(1, Math.min(100, Math.round(retries) || DEFAULT_RETRIES)))}
                title="How many times it is restarted after a crash before it is left down"
                class="w-14 shrink-0 bg-fjord-bg border border-fjord-border rounded-lg px-2 py-1 text-xs text-fjord-fg-secondary focus:outline-none focus:border-fjord-accent"
              />
            {/if}
          </div>
        {/each}
        {#if !onExit}
          <p class="mx-3 mt-1 pt-2 pb-1 border-t border-fjord-border text-xs text-fjord-fg-dim">{BOOT_ONLY_NOTE}</p>
        {/if}
      </div>
    {/if}
  </div>
{/if}
