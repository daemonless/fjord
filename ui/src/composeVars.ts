// What each ${VAR} in a compose (or director file) resolves to from the
// stack's .env, for the editor's inline values and the .env tab's "not set"
// list. Resolution follows expand.ts (and pkg/compose.ExpandEnv).

/**
 * parseEnv reads a .env the way the container sees it: KEY=VALUE lines,
 * comments and blanks skipped, one pair of surrounding quotes stripped --
 * podman-compose strips them, so KEY="a b" reaches the app as a b.
 */
export function parseEnv(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const eq = line.indexOf('=');
    if (eq <= 0) continue;
    let v = line.slice(eq + 1).trim();
    if (v.length >= 2 && (v[0] === '"' || v[0] === "'") && v[v.length - 1] === v[0]) v = v.slice(1, -1);
    out[line.slice(0, eq).trim()] = v;
  }
  return out;
}

export type VarRef = {
  from: number; // offset of "$"
  to: number; // offset just past "}"
  name: string;
  /** set: from .env; default: the ${X:-d} / ${X-d} fallback is in use;
   *  empty: set to ""; unset: nothing, and no default. */
  state: 'set' | 'default' | 'empty' | 'unset';
  value: string;
};

const REF = /\$\{([^}]+)\}/g;

/** refs finds every ${...} in text and resolves it against env. */
export function refs(text: string, env: Record<string, string>): VarRef[] {
  const out: VarRef[] = [];
  for (const m of text.matchAll(REF)) {
    const ref = m[1];
    const from = m.index ?? 0;
    const to = from + m[0].length;
    let name = ref;
    let def: string | undefined;
    let emptyUsesDefault = false;
    const iCol = ref.indexOf(':-');
    const iDash = ref.indexOf('-');
    if (iCol >= 0) {
      name = ref.slice(0, iCol);
      def = ref.slice(iCol + 2);
      emptyUsesDefault = true;
    } else if (iDash >= 0) {
      name = ref.slice(0, iDash);
      def = ref.slice(iDash + 1);
    }
    const has = name in env;
    const v = env[name] ?? '';
    let r: Pick<VarRef, 'state' | 'value'>;
    if (has && (v !== '' || !emptyUsesDefault)) r = v === '' ? { state: 'empty', value: '' } : { state: 'set', value: v };
    else if (def !== undefined) r = { state: 'default', value: def };
    else r = { state: 'unset', value: '' };
    out.push({ from, to, name, ...r });
  }
  return out;
}

/** missingVars: names the text uses with no default that env never sets. */
export function missingVars(text: string, env: Record<string, string>): string[] {
  const seen = new Set<string>();
  for (const r of refs(text, env)) if (r.state === 'unset') seen.add(r.name);
  return [...seen];
}

/** isSecret: a value not to show on screen (screenshots, shoulders). */
export const isSecret = (name: string) => /pass|secret|token|key|credential|auth/i.test(name);

/**
 * setEnvVar sets one variable in a .env's text: the first KEY= line is
 * replaced, else a line is added. Every other line -- comments, order,
 * blank lines -- stays as written. A value with spaces or # is quoted, which
 * podman-compose strips again.
 */
export function setEnvVar(text: string, name: string, value: string): string {
  const v = /[\s#"']/.test(value) ? `"${value.replace(/"/g, '\\"')}"` : value;
  const lines = text.split('\n');
  const i = lines.findIndex((l) => {
    const t = l.trim();
    return !t.startsWith('#') && t.slice(0, t.indexOf('=')).trim() === name && t.includes('=');
  });
  if (i >= 0) {
    lines[i] = `${name}=${v}`;
    return lines.join('\n');
  }
  const body = text.replace(/\s*$/, '');
  return (body ? body + '\n' : '') + `${name}=${v}\n`;
}

/** usedVars: each variable the text refers to, once, unset ones first. */
export function usedVars(text: string, env: Record<string, string>): VarRef[] {
  const byName = new Map<string, VarRef>();
  for (const r of refs(text, env)) if (!byName.has(r.name)) byName.set(r.name, r);
  const all = [...byName.values()];
  return [...all.filter((r) => r.state === 'unset'), ...all.filter((r) => r.state !== 'unset')];
}
