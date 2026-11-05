export type VarOption = { value: string; label: string };

export function isTrueFalse(s: unknown): boolean {
  const lower = String(s ?? '').trim().toLowerCase();
  return lower === 'true' || lower === 'false';
}

// A variable is a boolean toggle when declared as type: bool, or when its type
// is omitted/string and its default is "true" or "false".
export function isBoolVar(v: any): boolean {
  if (!v) return false;
  if (v.type === 'bool') return true;
  if (!v.type || v.type === 'string') return isTrueFalse(v.default);
  return false;
}

export function effectiveType(v: any): string {
  if (isBoolVar(v)) return 'bool';
  return v?.type || 'string';
}

export function boolValues(v: any): { on: string; off: string } {
  return {
    on: String(v?.on || 'true'),
    off: String(v?.off || 'false'),
  };
}

export function boolChecked(v: any, val: unknown): boolean {
  const { on, off } = boolValues(v);
  const raw = String(val ?? '').trim();
  const s = raw !== '' ? raw : String(v?.default ?? '').trim();
  if (!s) return false;
  const lower = s.toLowerCase();
  if (lower === off.toLowerCase() || lower === 'false') return false;
  return lower === on.toLowerCase() || lower === 'true';
}

export function boolToggled(v: any, checked: boolean): string {
  const { on, off } = boolValues(v);
  return checked ? on : off;
}

export function intProblem(v: any, val: unknown): string {
  const s = String(val ?? '').trim();
  if (!s) return '';
  if (!/^-?\d+$/.test(s)) return 'Must be a whole number';
  const n = Number(s);
  if (!Number.isSafeInteger(n)) return 'Must be a whole number';
  if (typeof v?.min === 'number' && n < v.min) return `Must be at least ${v.min}`;
  if (typeof v?.max === 'number' && n > v.max) return `Must be at most ${v.max}`;
  return '';
}

export function choiceOptions(v: any): VarOption[] {
  const raw: unknown[] = Array.isArray(v?.options) ? v.options : [];
  return raw
    .map((o: any): VarOption | null => {
      if (typeof o === 'string' || typeof o === 'number') {
        const s = String(o);
        return { value: s, label: s };
      }
      if (o && typeof o === 'object' && 'value' in o) {
        const value = String(o.value ?? '');
        const label = String(o.label ?? value);
        return { value, label };
      }
      return null;
    })
    .filter((o: VarOption | null): o is VarOption => !!o && o.value !== '');
}

export function choiceProblem(v: any, val: unknown): string {
  const s = String(val ?? '').trim();
  if (!s) return '';
  const opts = choiceOptions(v);
  if (!opts.length) return '';
  if (!opts.some((o) => o.value === s)) {
    return `Pick one of ${opts.map((o) => o.value).join(', ')}`;
  }
  return '';
}

const SIZE_RE = /^([1-9][0-9]*)([kmgtKMGT][bB]?)$/;

export function sizeUnits(fallback?: unknown): string[] {
  const m = SIZE_RE.exec(String(fallback ?? '').trim());
  const sample = m?.[2] ?? 'g';
  const upper = sample[0] === sample[0].toUpperCase();
  const suffix = sample.length > 1 ? sample.slice(1) : '';
  const base = upper ? ['K', 'M', 'G', 'T'] : ['k', 'm', 'g', 't'];
  return base.map((u) => u + suffix);
}

export function parseSize(val: unknown, fallback?: unknown): { amount: string; unit: string } {
  const units = sizeUnits(fallback);
  const defaultUnit = SIZE_RE.exec(String(fallback ?? '').trim())?.[2] ?? units[2];
  const s = String(val ?? '').trim();
  const m = /^(\d*)\s*([kmgtKMGT][bB]?)?$/.exec(s);
  if (m && (m[1] || m[2])) {
    const unit = m[2] || defaultUnit;
    return { amount: m[1], unit };
  }
  return { amount: s, unit: defaultUnit };
}

export function formatSize(amount: string, unit: string): string {
  const a = String(amount ?? '').trim();
  if (!a) return '';
  return `${a}${unit}`;
}

export function sizeProblem(val: unknown): string {
  const s = String(val ?? '').trim();
  if (!s) return '';
  if (!SIZE_RE.test(s)) return 'Enter a positive whole number and unit (e.g. 50g or 512m)';
  return '';
}

export function typedVarProblem(v: any, val: unknown): string {
  const t = effectiveType(v);
  if (t === 'int') return intProblem(v, val);
  if (t === 'choice') return choiceProblem(v, val);
  if (t === 'size') return sizeProblem(val);
  return '';
}
