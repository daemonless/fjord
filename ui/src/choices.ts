import * as yaml from 'js-yaml';

// One option of a stack choice as the manifest carries it (x-fjord.choices).
// drop names services the option removes; services is compose YAML for the
// ones it adds.
export type ChoiceOption = {
  id: string;
  drop?: string[];
  services?: string;
  env?: Record<string, string>;
  defaults?: Record<string, string>;
  secrets?: string[];
  ask?: { name: string }[];
};
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

/**
 * choiceVars are the variables a stack's choices set: what any option's env
 * fixes, fills by default, makes up as a secret, or asks for. The choice row
 * is their control. Listed again under Options they showed the catalog's
 * default -- Vikunja on MariaDB read VIKUNJA_DATABASE_TYPE "sqlite" -- and
 * took typing the option then overrode.
 */
export function choiceVars(choices: StackChoice[]): Set<string> {
  const out = new Set<string>();
  for (const c of choices) {
    for (const o of c.options) {
      for (const k of Object.keys(o.env ?? {})) out.add(k);
      for (const k of Object.keys(o.defaults ?? {})) out.add(k);
      for (const k of o.secrets ?? []) out.add(k);
      for (const a of o.ask ?? []) out.add(a.name);
    }
  }
  return out;
}

/** isToggle: a part that is just on or off, shown as one checkbox. */
export function isToggle(c: { kind?: string; options: { id: string }[] }): boolean {
  const ids = c.options.map((o) => o.id).sort();
  return c.kind === 'part' && ids.length === 2 && ids[0] === 'off' && ids[1] === 'on';
}

// A block body, at module level: wuchale's transform left an expression-bodied
// arrow inside pickedPhrase calling a runtime it never declared.
function and(xs: string[]): string {
  if (xs.length < 2) return xs.join('');
  return `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`;
}

/** pickedPhrase: the non-default answers for the summary line, in words:
 *  "without EffectCraft and DesignCraft", "with Public sharing", "MariaDB". */
export function pickedPhrase(
  choices: { id: string; kind?: string; label: string; default: string; options: { id: string; label: string }[] }[],
  picks: Record<string, string>,
): string {
  const changed = choices.filter((c) => picks[c.id] && picks[c.id] !== c.default);
  const off = changed.filter((c) => isToggle(c) && picks[c.id] === 'off').map((c) => c.label);
  const on = changed.filter((c) => isToggle(c) && picks[c.id] === 'on').map((c) => c.label);
  const other = changed.filter((c) => !isToggle(c)).map((c) => c.options.find((o) => o.id === picks[c.id])?.label ?? '').filter(Boolean);
  return [...other, ...(on.length ? [`with ${and(on)}`] : []), ...(off.length ? [`without ${and(off)}`] : [])].join(', ');
}

/**
 * askValue is what a field an option asks for starts as. A list with one
 * entry is no question: "Your own" database offering only PostgreSQL showed
 * a blank Kind to pick it from. Otherwise what was typed stays, and an
 * untouched field takes the manifest's default.
 */
export function askValue(a: { default?: string; values?: Record<string, string> }, current: string | undefined): string {
  const only = Object.keys(a.values ?? {});
  if (only.length === 1 && current !== only[0]) return only[0];
  return current ?? a.default ?? '';
}

/**
 * appjailMissing names the compose services the AppJail bundle has no jail
 * for, as the picks install them: the daemon refuses such an install
 * (appjailShort), and the wizard holds Install with the same reason instead
 * of after the click. Services an option adds bring their own jails (an
 * option without an AppJail form is held on its own), so only the compose's
 * own are checked. No bundle, or one that doesn't parse: nothing to say.
 */
export function appjailMissing(base: string[], choices: StackChoice[], picks: Record<string, string>, director: string): string[] {
  let jails: string[];
  try {
    jails = Object.keys(((yaml.load(director) as { services?: Record<string, unknown> }) ?? {}).services ?? {});
  } catch {
    return [];
  }
  if (!jails.length) return [];
  const kept = chosenServices(base, choices, picks);
  return base.filter((s) => kept.includes(s) && !jails.includes(s));
}
