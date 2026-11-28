<script lang="ts">
  // Per-stack Restore points control in Stack -> Settings:
  // Off | Before updates | Daily on ZFS-dataset stacks, Move to dataset / Set up ZFS
  // on plain-folder stacks, and hidden on hosts without ZFS.
  import { createEventDispatcher } from 'svelte';
  import { toast } from './toast';
  import { showTime } from './timeFormat';

  function formatHM(hm: string): string {
    const [h, m] = hm.split(':').map(Number);
    if (Number.isNaN(h) || Number.isNaN(m)) return hm;
    return showTime(new Date(2000, 0, 1, h, m, 0));
  }

  export let name: string;
  export let disabled = false;

  const dispatch = createEventDispatcher<{ changed: void }>();

  let loadedFor = '';
  let hasZfs = false;
  let dataset = '';
  let policy: 'off' | 'updates' | 'daily' = 'updates';
  let dailyTime = '02:00';
  let eligibleToMove = false;
  let saving = false;

  async function load(stackName: string) {
    loadedFor = stackName;
    try {
      const r = await fetch(`/api/stacks/${encodeURIComponent(stackName)}/restore-points`);
      if (!r.ok || loadedFor !== stackName) return;
      const d = await r.json();
      hasZfs = !!d.hasZfs;
      dataset = d.dataset || '';
      if (d.policy === 'off' || d.policy === 'updates' || d.policy === 'daily') policy = d.policy;
      else policy = 'updates';
      if (d.dailyTime) dailyTime = d.dailyTime;
      eligibleToMove = false;
      if (hasZfs && !dataset) {
        const mr = await fetch(`/api/stacks/${encodeURIComponent(stackName)}/migrate-dataset`).catch(() => null);
        if (mr?.ok && loadedFor === stackName) {
          const md = await mr.json();
          eligibleToMove = !!md.eligible;
        }
      }
    } catch {}
  }

  $: if (name && name !== loadedFor) load(name);

  async function setPolicy(next: 'off' | 'updates' | 'daily') {
    if (saving || disabled || next === policy) return;
    const prev = policy;
    policy = next;
    saving = true;
    try {
      const r = await fetch(`/api/stacks/${encodeURIComponent(name)}/restore-points/policy`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ policy: next }),
      });
      if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
      dispatch('changed');
    } catch (e: any) {
      policy = prev;
      toast(`Restore points: ${e.message}`, { kind: 'error' });
    } finally {
      saving = false;
    }
  }
</script>

{#if hasZfs}
  <span class="text-fjord-fg-muted self-start pt-1">Restore points</span>
  <div class="flex flex-col gap-1.5" data-restore-point-policy>
    {#if dataset}
      <div class="inline-flex w-fit rounded-lg border border-fjord-border bg-fjord-inset p-0.5 text-xs">
        {#each [
          { id: 'off', label: 'Off' },
          { id: 'updates', label: 'Before updates' },
          { id: 'daily', label: 'Daily' },
        ] as opt (opt.id)}
          <button
            type="button"
            disabled={disabled || saving}
            on:click={() => setPolicy(opt.id as 'off' | 'updates' | 'daily')}
            class="rounded-md px-2.5 py-1 transition-colors disabled:opacity-40 {policy === opt.id
              ? 'bg-fjord-card font-semibold text-fjord-fg shadow-xs'
              : 'text-fjord-fg-muted hover:text-fjord-fg'}"
          >{opt.label}</button>
        {/each}
      </div>
      <p class="text-xs text-fjord-fg-dim max-w-prose">
        {#if policy === 'off'}
          No automatic restore points. You can still save one by hand in History.
        {:else if policy === 'daily'}
          Saved automatically before updates and daily at {formatHM(dailyTime)}.
        {:else}
          Saved automatically before each update.
        {/if}
      </p>
    {:else}
      <div class="flex flex-wrap items-center gap-2">
        <span class="text-xs text-fjord-fg-dim">Off — uses a plain folder instead of its own ZFS dataset.</span>
        {#if eligibleToMove}
          <a
            href="#/stacks/{encodeURIComponent(name)}/history"
            class="text-xs font-medium text-fjord-accent hover:underline"
          >Move to dataset</a>
        {:else}
          <a
            href="#/settings/maintenance"
            class="text-xs font-medium text-fjord-accent hover:underline"
          >Set up ZFS</a>
        {/if}
      </div>
    {/if}
  </div>
{/if}
