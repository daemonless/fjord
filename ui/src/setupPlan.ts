// What the setup page asks of the operator, from the doctor's checks for the
// engines they picked: what fjord installs, what they must do by hand, and
// what is already fine.

export type Check = {
  id: string;
  name: string;
  engine?: string;
  group?: string; // "" = the engine itself, "networking", "firewall", "extras"
  optional?: boolean;
  action?: string; // what Install does, in plain words
  commands?: string[]; // exactly what fjord runs for it
  status: 'ok' | 'warn' | 'fail' | 'unknown';
  detail?: string;
  why?: string;
  fix?: string;
  installable?: boolean;
};

export type Plan = {
  auto: Check[]; // fjord installs these with one button, in this order
  manual: Check[]; // need a person (a firewall edit); not optional
  extras: Check[]; // optional and not ok: offered, never a problem
  ok: Check[];
  all: Check[]; // every check for the chosen engines, in setup order
  ready: boolean; // nothing required is left
};

// A check whose id is an engine's name is that engine being installed
// ("podman" CLI, "appjail"): it belongs to that engine's plan only.
export function plan(checks: Check[], engine: string | string[], engineNames: string[]): Plan {
  const chosen = Array.isArray(engine) ? engine : [engine];
  const mine = checks.filter((c) => (engineNames.includes(c.id) ? chosen.includes(c.id) : !c.engine || chosen.includes(c.engine)));
  const notOk = mine.filter((c) => c.status !== 'ok');
  const auto = notOk.filter((c) => c.installable).sort((a, b) => stepOrder(a, engineNames) - stepOrder(b, engineNames));
  const manual = notOk.filter((c) => !c.installable && !c.optional);
  const extras = notOk.filter((c) => !c.installable && c.optional);
  const ok = mine.filter((c) => c.status === 'ok');
  const ready = !notOk.some((c) => !c.optional);
  const all = [...mine].sort((a, b) => stepOrder(a, engineNames) - stepOrder(b, engineNames));
  return { auto, manual, extras, ok, all, ready };
}

// stepOrder sorts the setup steps the way a person would do them: one engine
// at a time, and within it the engine, its other packages, downloads, then
// starting its services (which need everything installed); optional last.
export function stepOrder(c: Check, engineNames: string[]): number {
  const owner = engineNames.includes(c.id) ? c.id : c.engine || '';
  const e = owner ? engineNames.indexOf(owner) : engineNames.length;
  const cmds = c.commands || [];
  const kind = engineNames.includes(c.id)
    ? 0
    : cmds.some((x) => x.startsWith('service '))
      ? 3
      : cmds.some((x) => x.startsWith('pkg install'))
        ? 1
        : 2;
  return e * 100 + (c.optional ? 10 : 0) + kind;
}

export async function fetchChecks(engine: string | string[]): Promise<{ checks: Check[]; host: boolean }> {
  const q = (Array.isArray(engine) ? engine : [engine]).join(',');
  const r = await fetch(`/api/setup?engine=${encodeURIComponent(q)}`);
  if (!r.ok) throw new Error((await r.text()).trim() || `HTTP ${r.status}`);
  const rep = await r.json();
  return { checks: rep.checks || [], host: rep.mode === 'host' };
}

// purpose is what a check is for, in a few words: the first sentence of its why.
export function purpose(c: Check): string {
  const first = (c.why || '').split(/(?<=\.)\s/)[0] || '';
  return first.replace(/\.$/, '');
}

// installStream runs one check's installer, handing its terminal output to
// out as it arrives. The daemon ends the stream with "[done]" or
// "[error] <why>" -- the HTTP status is sent before the outcome is known.
export async function installStream(id: string, out: (text: string) => void): Promise<string | null> {
  const r = await fetch('/api/setup/install?stream=1', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id }),
  });
  if (!r.ok) {
    const msg = (await r.text()).trim() || `HTTP ${r.status}`;
    out(`[error] ${msg}\n`);
    return msg;
  }
  let all = '';
  const reader = r.body?.getReader();
  const decoder = new TextDecoder('utf-8');
  if (reader) {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      const text = decoder.decode(value, { stream: true });
      all += text;
      out(text);
    }
  }
  return outcome(all);
}

// outcome reads the end of an install stream: null when it finished, else why not.
export function outcome(all: string): string | null {
  const last = all.trimEnd().split('\n').pop() || '';
  if (last === '[done]') return null;
  if (last.startsWith('[error]')) return last.slice('[error]'.length).trim();
  return 'the connection ended before the install finished';
}
