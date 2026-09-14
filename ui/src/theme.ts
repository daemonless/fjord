// Theme preference. The <html data-theme> attribute is the single source of
// truth for the CSS; index.html stamps it before first paint so there is no
// flash, and this module keeps it in sync afterwards.
//
// Storage holds only an explicit choice: "light"/"dark" when the user picked
// one, nothing at all while they are following the OS. That distinction is
// what lets a machine set to auto-switch keep working after a visit here.

export type Theme = 'light' | 'dark';

const KEY = 'fjord-theme';

// Browsers in private mode (and some embedded webviews) throw on storage
// access rather than returning null, so every read and write is guarded.
function stored(): Theme | null {
  try {
    const v = localStorage.getItem(KEY);
    return v === 'light' || v === 'dark' ? v : null;
  } catch {
    return null;
  }
}

function systemTheme(): Theme {
  try {
    return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  } catch {
    return 'dark';
  }
}

/** The theme in effect: the explicit choice if there is one, else the OS. */
export function currentTheme(): Theme {
  return stored() ?? systemTheme();
}

/** True while no explicit choice is stored, i.e. the OS is driving. */
export function followingSystem(): boolean {
  return stored() === null;
}

function apply(t: Theme) {
  document.documentElement.setAttribute('data-theme', t);
}

/** Record an explicit choice and apply it. */
export function setTheme(t: Theme) {
  try {
    localStorage.setItem(KEY, t);
  } catch {
    // Not persistable (private mode) -- still apply it for this page.
  }
  apply(t);
}

/** Drop the explicit choice and fall back to the OS preference. */
export function clearTheme() {
  try {
    localStorage.removeItem(KEY);
  } catch {
    // ignore
  }
  apply(systemTheme());
}

/**
 * Track the OS preference while no explicit choice is stored, so a host that
 * flips at sunset flips fjord with it. Returns an unsubscribe function.
 */
export function watchSystem(onChange?: (t: Theme) => void): () => void {
  let mq: MediaQueryList;
  try {
    mq = window.matchMedia('(prefers-color-scheme: light)');
  } catch {
    return () => {};
  }
  const handler = () => {
    if (!followingSystem()) return; // an explicit choice wins
    const t = systemTheme();
    apply(t);
    onChange?.(t);
  };
  mq.addEventListener('change', handler);
  return () => mq.removeEventListener('change', handler);
}
