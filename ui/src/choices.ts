import * as yaml from 'js-yaml';

// One option of a stack choice as the manifest carries it (x-fjord.choices).
// drop names services the option removes; services is compose YAML for the
// ones it adds.
export type ChoiceOption = { id: string; drop?: string[]; services?: string };
export type StackChoice = { id: string; default: string; options: ChoiceOption[] };

/**
 * chosenServices is the stack's services as the picks install them: the
 * compose's own, less what a picked option drops, plus what it adds. The
 * same rule as the daemon's ApplyChoices, so the wizard's rows are the
 * services that will exist -- not a machine-learning row for a stack
 * installed without it, and no row for the proxy it was installed with.
 */
export function chosenServices(base: string[], choices: StackChoice[], picks: Record<string, string>): string[] {
  const dropped = new Set<string>();
  const added: string[] = [];
  for (const c of choices) {
    const o = c.options.find((x) => x.id === (picks[c.id] || c.default));
    for (const d of o?.drop ?? []) dropped.add(d);
    if (!o?.services) continue;
    try {
      added.push(...Object.keys((yaml.load(o.services) as Record<string, unknown>) ?? {}));
    } catch {
      // The daemon refuses a bad option at install; here it just adds no row.
    }
  }
  const out = base.filter((s) => !dropped.has(s));
  for (const s of added) if (!out.includes(s)) out.push(s);
  return out;
}
