<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import MenuField from './MenuField.svelte';
  import { toast } from './toast';
  import type { StackRecovery } from './stacks';

  export let name: string;
  export let recovery: StackRecovery | undefined;
  export let disabled = false;
  /** Id of the visible line that says why the control is locked. */
  export let describedBy = '';

  const dispatch = createEventDispatcher<{ changed: void }>();
  let open = false;
  let saving = false;
  let maxTries = 3;
  let cooldown = 60;

  $: action = recovery?.action || 'none';
  $: if (!open && !saving && recovery?.maxTries) maxTries = recovery.maxTries;
  $: if (!open && !saving && recovery?.cooldown) cooldown = recovery.cooldown;
  $: off = disabled || saving;
  $: if (off && !saving) open = false;

  function shownLabel(r: StackRecovery | undefined): string {
    const a = r?.action || 'none';
    if (a === 'restart') {
      const cap = r?.maxTries || 3;
      if (r?.attempts) return `Restart stack (${r.attempts}/${cap})`;
      return `Restart stack (${cap}×)`;
    }
    if (a === 'stop') return 'Stop stack';
    return 'Do nothing';
  }

  function shownTitle(r: StackRecovery | undefined): string {
    const a = r?.action || 'none';
    if (a === 'restart') {
      return `On a failed background health check, restart up to ${r?.maxTries || 3} times (${r?.cooldown || 60}s cooldown).`;
    }
    if (a === 'stop') {
      return 'On a failed background health check, stop the stack so it does not keep looping.';
    }
    return 'On a failed background health check, do nothing and report the failure.';
  }

  // Reactive, not a function called in the markup: that would keep the
  // sentence from redrawing as the numbers change.
  $: choices = choicesFor(maxTries, cooldown);
  function choicesFor(maxTries: number, cooldown: number) {
    return [
      {
        value: 'none',
        label: 'Do nothing',
        meaning: 'Report the failure on the dashboard and stack page without touching the containers.',
      },
      {
        value: 'restart',
        label: 'Restart stack',
        meaning: `Restart the stack up to ${maxTries} ${maxTries === 1 ? 'time' : 'times'}, with ${cooldown}s cooldown between attempts.`,
      },
      {
        value: 'stop',
        label: 'Stop stack',
        meaning: 'Stop the stack on a failed check so a crash loop does not keep running.',
      },
    ];
  }

  function fmtEventTime(at: string): string {
    const d = new Date(at);
    const sameDay = d.toDateString() === new Date().toDateString();
    return sameDay
      ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
      : d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  }

  // What fjordd accepts. The field is set to it too, so it never shows a
  // number other than the one saved.
  const MAX_TRIES = 20;
  const clampTries = (n: number) => Math.max(1, Math.min(MAX_TRIES, Math.round(n) || 3));
  const clampCooldown = (n: number) => Math.max(10, Math.min(3600, Math.round(n) || 60));

  async function save(nextAction: string, tries = maxTries, wait = cooldown) {
    tries = maxTries = clampTries(tries);
    wait = cooldown = clampCooldown(wait);
    saving = true;
    try {
      const res = await fetch(`/api/stacks/${encodeURIComponent(name)}/recovery-policy`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          action: nextAction,
          maxTries: tries,
          cooldown: wait,
        }),
      });
      if (!res.ok) {
        toast(`Recovery policy not saved: ${(await res.text()).trim()}`, { kind: 'error' });
        return;
      }
      if (nextAction !== 'restart') open = false;
      dispatch('changed');
    } catch {
      toast('Recovery policy not saved: fjordd did not answer', { kind: 'error' });
    } finally {
      saving = false;
    }
  }
</script>

{#if recovery}
  <MenuField
    data-recovery-policy
    bind:open
    label={shownLabel(recovery)}
    title={shownTitle(recovery)}
    disabled={off}
    {describedBy}
    buttonAttrs={{ 'data-recovery': action }}
  >
    {#each choices as c}
      <div class="flex flex-col gap-1.5 px-3 py-2 hover:bg-fjord-border/50 {action === c.value ? 'bg-fjord-border/30' : ''}">
        <button
          type="button"
          role="menuitemradio"
          aria-checked={action === c.value}
          disabled={saving}
          on:click={() => action !== c.value && save(c.value)}
          class="flex items-start gap-2 text-left"
        >
          <span
            class="mt-1 w-3 h-3 shrink-0 rounded-full border {action === c.value
              ? 'border-fjord-accent bg-fjord-accent shadow-[inset_0_0_0_2px_var(--color-fjord-card)]'
              : 'border-fjord-fg-faint'}"
          ></span>
          <span class="flex flex-col flex-1">
            <span class="text-sm font-medium text-fjord-fg-body">{c.label}</span>
            <span class="text-xs text-fjord-fg-dim">{c.meaning}</span>
          </span>
        </button>
        {#if c.value === 'restart' && action === 'restart'}
          <div class="flex items-center gap-3 pl-5 pt-1 text-xs text-fjord-fg-dim">
            <label class="flex items-center gap-1.5">
              <span>Max attempts</span>
              <input
                type="number"
                aria-label="Maximum recovery attempts"
                min="1"
                max={MAX_TRIES}
                bind:value={maxTries}
                disabled={saving}
                on:change={() => save('restart', maxTries, cooldown)}
                class="w-14 bg-fjord-bg border border-fjord-border rounded-lg px-2 py-1 text-xs text-fjord-fg-secondary focus:outline-none focus:border-fjord-accent"
              />
            </label>
            <label class="flex items-center gap-1.5">
              <span>Cooldown (s)</span>
              <input
                type="number"
                aria-label="Cooldown seconds between attempts"
                min="10"
                max="3600"
                step="10"
                bind:value={cooldown}
                disabled={saving}
                on:change={() => save('restart', maxTries, cooldown)}
                class="w-16 bg-fjord-bg border border-fjord-border rounded-lg px-2 py-1 text-xs text-fjord-fg-secondary focus:outline-none focus:border-fjord-accent"
              />
            </label>
          </div>
        {/if}
      </div>
    {/each}
    {#if recovery.events?.length}
      <div class="mx-3 mt-1 pt-2 pb-1 border-t border-fjord-border text-xs text-fjord-fg-dim">
        <div class="font-medium text-fjord-fg-secondary mb-1">Recent health events</div>
        <div class="max-h-32 overflow-y-auto space-y-1">
          {#each [...recovery.events].reverse().slice(0, 5) as ev}
            <div class="flex items-baseline justify-between gap-2">
              <span class="truncate text-fjord-fg-body" title={ev.message || ev.check}>{ev.message || ev.check}</span>
              <span class="shrink-0 text-[11px] text-fjord-fg-muted">{fmtEventTime(ev.at)}</span>
            </div>
          {/each}
        </div>
      </div>
    {/if}
  </MenuField>
{/if}
