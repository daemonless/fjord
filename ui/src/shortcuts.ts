// Keyboard shortcuts: one table drives the handler (App.svelte) and the
// overlay (Shortcuts.svelte), so the two cannot drift apart. Keys are
// single characters; "g" starts a two-key chord ("g s").
export type Shortcut = { keys: string; does: string; go?: string; action?: 'search' | 'refresh' | 'help' | 'close' };

export const shortcuts: Shortcut[] = [
  { keys: '?', does: 'Show these shortcuts', action: 'help' },
  { keys: '/', does: 'Search stacks, or apps in the store', action: 'search' },
  { keys: 'g s', does: 'Go to Stacks', go: '#/stacks' },
  { keys: 'g a', does: 'Go to the App Store', go: '#/store' },
  { keys: 'g n', does: 'Go to Networks', go: '#/networks' },
  { keys: 'g v', does: 'Go to Volumes', go: '#/volumes' },
  { keys: 'g y', does: 'Go to System', go: '#/system' },
  { keys: 'g ,', does: 'Go to Settings', go: '#/settings' },
  { keys: 'r', does: 'Refresh', action: 'refresh' },
  { keys: 'Esc', does: 'Close a dialog, or this', action: 'close' },
];

// Elsewhere already, listed so the overlay tells the whole story.
export const alsoShortcuts: Shortcut[] = [
  { keys: 'Ctrl F', does: 'Find in the log view' },
  { keys: '← → ↑ ↓', does: 'Resize the sidebar and the output drawer (when focused)' },
];

/** True while the key press belongs to a field: typing is never a shortcut. */
export function typing(e: KeyboardEvent): boolean {
  const t = e.target as HTMLElement | null;
  if (!t) return false;
  const tag = t.tagName;
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || t.isContentEditable;
}

let chord = '';
let chordTimer: ReturnType<typeof setTimeout> | undefined;

/** The shortcut a key press matches, or null; keeps the "g" chord between
 *  presses for 1.5 s. Modified keys (Ctrl, Alt, Meta) are never ours. */
export function match(e: KeyboardEvent): Shortcut | null {
  if (e.ctrlKey || e.altKey || e.metaKey) return null;
  const key = e.key === 'Escape' ? 'Esc' : e.key;
  if (chord) {
    const want = `${chord} ${key}`;
    chord = '';
    clearTimeout(chordTimer);
    return shortcuts.find((s) => s.keys === want) || null;
  }
  if (key === 'g') {
    chord = 'g';
    clearTimeout(chordTimer);
    chordTimer = setTimeout(() => (chord = ''), 1500);
    return null;
  }
  return shortcuts.find((s) => s.keys === key) || null;
}
