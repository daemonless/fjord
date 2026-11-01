// A mount's options: what follows its container path ("ro,z"). The host says
// which it offers by name (/api/mount-options: read-only, the SELinux labels
// where SELinux is on, U); any other podman option is typed.
export type MountOption = { name: string; label: string; help: string };

let offered: Promise<MountOption[]> | null = null;

export function offeredMountOptions(): Promise<MountOption[]> {
  offered ??= fetch('/api/mount-options')
    .then((r) => (r.ok ? r.json() : { offered: [] }))
    .then((j) => j.offered ?? [])
    .catch(() => {
      offered = null; // try again next time
      return [{ name: 'ro', label: 'Read-only', help: '' }];
    });
  return offered;
}

// parseOptions reads typed options, comma or space separated, once each.
export function parseOptions(text: string): string[] {
  const out: string[] = [];
  for (const o of text.split(/[\s,]+/)) if (o && !out.includes(o)) out.push(o);
  return out;
}

// otherOptions is the typed part: the options with no checkbox of their own.
export function otherOptions(opts: string[], shown: MountOption[]): string {
  return opts.filter((o) => !shown.some((s) => s.name === o)).join(',');
}

// combine is the checked options in the order they are offered, then the
// typed ones. z and Z exclude each other: the last one checked wins.
export function combine(checked: string[], typed: string, shown: MountOption[]): string[] {
  const out = shown.map((s) => s.name).filter((n) => checked.includes(n));
  for (const o of parseOptions(typed)) if (!out.includes(o)) out.push(o);
  return out;
}

// withOption adds name to opts, dropping the label it replaces (z for Z).
export function withOption(opts: string[], name: string): string[] {
  const other = name === 'z' ? 'Z' : name === 'Z' ? 'z' : '';
  const out = opts.filter((o) => o !== other && o !== name);
  out.push(name);
  return out;
}
