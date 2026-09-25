<script lang="ts">
  // The variables a compose (or director file) uses, beside the editor: what
  // each is set to, which are missing, and a field to set each -- written
  // back into the stack's .env, which stays editable as a whole on its tab.
  import { createEventDispatcher, tick } from 'svelte';
  import { parseEnv, usedVars, isSecret } from './composeVars';
  import Icon from './Icon.svelte';

  export let text = ''; // the compose or director file
  export let env = ''; // the .env's text
  // A label clicked in the editor: that variable's field is focused.
  export let focus = '';

  const dispatch = createEventDispatcher<{ set: { name: string; value: string }; close: void }>();

  // Missing first -- but the order follows the file, not each keystroke:
  // typing a value into a missing variable must not move the row away from
  // under the cursor. orderFor reads env without the $: seeing it.
  function orderFor(t: string) {
    return usedVars(t, parseEnv(env)).map((v) => v.name);
  }
  $: order = orderFor(text);
  $: current = new Map(usedVars(text, parseEnv(env)).map((v) => [v.name, v]));
  $: vars = order.map((n) => current.get(n)).filter((v): v is NonNullable<typeof v> => !!v);
  $: missing = vars.filter((v) => v.state === 'unset').length;
  let rows: Record<string, HTMLInputElement> = {};
  let flash = '';

  $: if (focus) focusRow(focus);
  async function focusRow(name: string) {
    await tick();
    const el = rows[name];
    if (!el) return;
    el.scrollIntoView({ block: 'nearest' });
    el.focus();
    el.select();
    flash = name;
    setTimeout(() => flash === name && (flash = ''), 1200);
    focus = '';
  }
</script>

<aside class="w-72 shrink-0 h-full flex flex-col border-l border-fjord-border bg-fjord-card/40 text-sm">
  <div class="shrink-0 flex items-center gap-2 px-3 py-2 border-b border-fjord-border">
    <span class="font-semibold text-fjord-fg">Variables</span>
    <span class="text-xs {missing ? 'text-fjord-warning' : 'text-fjord-fg-dim'}">
      {vars.length ? (missing ? `${missing} missing` : 'all set') : ''}
    </span>
    <span class="flex-1"></span>
    <button on:click={() => dispatch('close')} title="Hide variables" class="text-fjord-fg-muted hover:text-fjord-fg"
      ><Icon name="close" size={14} /></button
    >
  </div>
  <div class="flex-1 overflow-y-auto px-3 py-2 space-y-2">
    {#if !vars.length}
      <p class="text-xs text-fjord-fg-dim">This file uses no <span class="font-mono">${'{'}…{'}'}</span> variables.</p>
    {/if}
    {#each vars as v (v.name)}
      <label class="block rounded-lg px-1.5 py-1 transition-colors {flash === v.name ? 'bg-fjord-accent/20' : ''}">
        <span class="flex items-center gap-1.5 text-xs">
          {#if v.state === 'unset'}
            <span class="text-fjord-warning" title="not set, no default">⚠</span>
          {:else if v.state === 'set'}
            <span class="text-fjord-success" title="set in .env">✓</span>
          {:else}
            <span class="text-fjord-fg-dim" title={v.state === 'empty' ? 'set, but empty' : 'the default is used'}>◦</span>
          {/if}
          <span class="font-mono text-fjord-fg-secondary truncate">{v.name}</span>
          {#if v.state === 'default'}<span class="text-fjord-fg-dim">default</span>{/if}
        </span>
        <input
          bind:this={rows[v.name]}
          type={isSecret(v.name) ? 'password' : 'text'}
          value={v.state === 'default' ? '' : v.value}
          placeholder={v.state === 'default' ? v.value : v.state === 'unset' ? 'not set' : ''}
          on:input={(e) => dispatch('set', { name: v.name, value: e.currentTarget.value })}
          class="mt-0.5 w-full bg-fjord-inset border rounded-md px-2 py-1 font-mono text-xs text-fjord-fg-body {v.state === 'unset'
            ? 'border-fjord-warning/60'
            : 'border-fjord-border'}"
        />
      </label>
    {/each}
  </div>
  <p class="shrink-0 px-3 py-2 border-t border-fjord-border text-[11px] text-fjord-fg-dim">
    Saved to this stack's .env with Save. The whole file is on the .env tab.
  </p>
</aside>
