// A stack's restart policy in words, for the chip in the stack header, the
// stacks list, the install wizard and Settings. fjordd sends it as the stack
// detail's `restart`, with what the stack's engine offers: an engine that
// restarts on exit takes the four compose policies; one that does not only
// decides what starts at boot, and is worded as that.

export type StackRestart = {
  policy: string; // unless-stopped | always | on-failure | no | "" (not set)
  retries?: number;
  mixed?: boolean;
  services: Record<string, string>;
  policies: string[]; // what the engine offers; none = not available
  onExit: boolean; // the engine restarts what exits
};

export type RestartChoice = { value: string; label: string };

export const DEFAULT_RETRIES = 5;

// The words lead; on an engine that restarts on exit the compose value is
// shown beside them, as the compose tab spells it.
const ON_EXIT: Record<string, string> = {
  'unless-stopped': 'Restart unless stopped',
  always: 'Always restart',
  'on-failure': 'Restart after a crash',
  no: "Don't restart",
};
const BOOT_ONLY: Record<string, string> = {
  'unless-stopped': 'Start at boot',
  always: 'Always start at boot',
  no: "Don't start at boot",
};

export const BOOT_ONLY_NOTE = 'This engine does not restart an app when it stops; this sets what starts at boot.';
export const NOT_AVAILABLE_NOTE = 'This engine does not take a restart policy from fjord.';

/** The choices an engine offers, in the order they are listed. */
export function choicesFor(policies: string[], onExit: boolean): RestartChoice[] {
  const words = onExit ? ON_EXIT : BOOT_ONLY;
  return Object.keys(words)
    .filter((v) => policies.includes(v))
    .map((v) => ({ value: v, label: words[v] }));
}

/** The compose spelling of a choice: on-failure carries its count. */
export function composeValue(policy: string, retries = 0): string {
  return policy === 'on-failure' && retries > 0 ? `${policy}:${retries}` : policy;
}

/** The chip's words for the current setting. */
export function restartShort(r: StackRestart | undefined): string {
  if (!r) return '';
  if (!r.policies.length) return 'Restart: not available';
  if (r.mixed) return 'Restart: mixed';
  if (!r.onExit) return BOOT_ONLY[r.policy || 'unless-stopped'] ?? 'Start at boot';
  if (r.policy === 'on-failure') return `Restart after a crash (${r.retries || DEFAULT_RETRIES}×)`;
  return ON_EXIT[r.policy] ?? 'Restart: not set';
}

/** What the policy does, in one sentence: when it exits, and at boot. */
export function restartMeaning(policy: string, retries = 0, onExit = true): string {
  if (!onExit) {
    switch (policy) {
      case 'always':
        return 'Started at boot, even if you stopped it.';
      case 'no':
        return 'Not started at boot: only you start it.';
      default: // unless-stopped, and a stack from before fjord set one
        return 'Started at boot, unless you stopped it.';
    }
  }
  switch (policy) {
    case 'unless-stopped':
      return 'Restarted whenever it exits. Started at boot, unless you stopped it.';
    case 'always':
      return 'Restarted whenever it exits. Started at boot, even if you stopped it.';
    case 'on-failure':
      return `Restarted when it exits with an error${retries > 0 ? `, up to ${retries} times` : ''}. Started at boot, unless you stopped it.`;
    case 'no':
      return 'Left down when it exits. Not started at boot.';
    default:
      return 'Not set: left down when it exits, started at boot if it was running.';
  }
}

/** The whole story for a stack, for a hover. */
export function restartTitle(r: StackRestart | undefined): string {
  if (!r) return '';
  if (!r.policies.length) return NOT_AVAILABLE_NOTE;
  if (r.mixed) return mixedMeaning(r.services);
  const m = restartMeaning(r.policy, r.retries, r.onExit);
  return r.onExit ? m : `${m} ${BOOT_ONLY_NOTE}`;
}

/** The mixed case, named service by service. */
export function mixedMeaning(services: Record<string, string>): string {
  const parts = Object.entries(services)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([svc, v]) => `${svc}: ${v || 'not set'}`);
  return `Its services differ (${parts.join(', ')}). Pick one to set them all.`;
}
