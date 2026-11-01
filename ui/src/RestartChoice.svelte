<script lang="ts">
  // A restart policy picked in a form: the install wizard's Options, and the
  // defaults in Settings. The stack page has its own chip (RestartPolicy).
  // The choices are the engine's (see restartPolicy.ts).
  import { DEFAULT_RETRIES, bootOnlyNote, choicesFor, restartMeaning } from './restartPolicy';

  export let id: string;
  export let policies: string[] = [];
  export let onExit = true;
  export let policy = 'unless-stopped';
  export let retries = DEFAULT_RETRIES;
  export let disabled = false;
  /** Shown instead of the meaning when disabled. */
  export let note = '';

  $: choices = choicesFor(policies, onExit);
</script>

<div class="flex items-center gap-2">
  <select
    {id}
    bind:value={policy}
    on:change
    {disabled}
    class="flex-1 bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent disabled:opacity-50 disabled:cursor-not-allowed"
  >
    {#each choices as c}<option value={c.value}>{c.label}{onExit ? ` (${c.value})` : ''}</option>{/each}
  </select>
  {#if onExit && policy === 'on-failure'}
    <input
      type="number"
      aria-label="Restarts after a crash"
      min="1"
      max="100"
      bind:value={retries}
      on:change={() => (retries = Math.max(1, Math.min(100, Math.round(retries) || DEFAULT_RETRIES)))}
      on:change
      {disabled}
      title="How many times it is restarted after a crash before it is left down"
      class="w-16 bg-fjord-inset border border-fjord-border rounded-md px-2 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent disabled:opacity-50"
    />
    <span class="text-sm text-fjord-fg-muted">times</span>
  {/if}
</div>
<p class="text-xs text-fjord-fg-dim mt-1">
  {#if disabled && note}{note}{:else}{restartMeaning(policy, policy === 'on-failure' ? retries : 0, onExit)}{#if !onExit}
      {bootOnlyNote()}{/if}{/if}
</p>
