import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { compile } from 'svelte/compiler';
import { describe, expect, it } from 'vitest';

// Svelte builds a legacy `$:` block's dependency list from what the block
// REFERENCES. A plain `const` closure that reads reactive state hides that
// dependency, and the block compiles to `legacy_pre_effect(() => {}, ...)` --
// no dependencies at all. It then runs once, at first render, and never again.
//
// That has shipped three times in this codebase:
//   - isDefault     -> the Set default button kept its old state
//   - enginesFor    -> host and none vanished from the Networks page, because
//                      visibleBuiltIn was decided before /api/networks/kinds
//                      had answered
//   - rowIPProblem  -> a stack's network table said nothing about an address
//                      when it rendered before /api/networks answered
//
// An empty dependency list is the fingerprint, so assert there aren't any.
// A reactive statement that genuinely depends on nothing wants to be a const.
const SRC = new URL('.', import.meta.url).pathname;

function components(): string[] {
  return readdirSync(SRC).filter((f) => f.endsWith('.svelte')).sort();
}

describe('svelte reactive statements declare their dependencies', () => {
  const found = components();

  it('finds components to check', () => {
    expect(found.length).toBeGreaterThan(5);
  });

  for (const file of found) {
    it(file, () => {
      const source = readFileSync(join(SRC, file), 'utf8');
      const js = compile(source, { generate: 'client', runes: false, filename: file }).js.code;
      // `legacy_pre_effect(() => {}, ...)` -- a $: block tracking nothing.
      const blind = js.match(/legacy_pre_effect\(\(\) => \{\},/g) ?? [];
      expect(blind, `${file}: ${blind.length} reactive block(s) depend on nothing -- a const closure is probably hiding the dependency`).toHaveLength(0);
    });
  }
});
