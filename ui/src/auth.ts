import { writable } from 'svelte/store';

// What /api/auth/state says: whether an account exists, whether this
// browser is signed in, and whether sign-in is off (FJORD_AUTH=none).
export type AuthState = {
  off: boolean;
  account: boolean;
  signedIn: boolean;
  user?: string;
  setupCodePath?: string;
};

// null until the first answer; 'unreachable' when fjordd did not answer.
export const auth = writable<AuthState | null | 'unreachable'>(null);

const plainFetch = window.fetch.bind(window);

export async function loadAuth(): Promise<void> {
  try {
    const r = await plainFetch('/api/auth/state', { cache: 'no-store' });
    if (!r.ok) throw new Error(String(r.status));
    auth.set(await r.json());
  } catch {
    auth.set('unreachable');
  }
}

function isGuarded(input: RequestInfo | URL): boolean {
  const u = new URL(input instanceof Request ? input.url : String(input), location.href);
  return u.origin === location.origin && (u.pathname.startsWith('/api/') || u.pathname.startsWith('/catalog/')) && !u.pathname.startsWith('/api/auth/');
}

// Every fetch in the UI goes through window.fetch: a 401 from fjord means
// the session ended (signed out elsewhere, password changed, 30 days
// unused), and the page asks to sign in again over what is open.
export function guardFetch(): void {
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const r = await plainFetch(input, init);
    if (r.status === 401 && isGuarded(input)) {
      auth.update((s) => (s && s !== 'unreachable' ? { ...s, signedIn: false } : s));
    }
    return r;
  };
}

// The event stream and the shell are not fetches: when one closes, ask.
export function checkSession(): void {
  loadAuth();
}

// Fired after signing in again over an open page, so it reloads what failed.
export const SIGNED_IN = 'fjord:signed-in';

export async function postJSON(path: string, body: unknown): Promise<{ ok: boolean; status: number; error: string }> {
  try {
    const r = await plainFetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    let error = '';
    if (!r.ok) {
      const t = await r.text();
      try {
        error = JSON.parse(t).error ?? t;
      } catch {
        error = t;
      }
    }
    return { ok: r.ok, status: r.status, error: error.trim() };
  } catch {
    return { ok: false, status: 0, error: 'fjord did not answer' };
  }
}
