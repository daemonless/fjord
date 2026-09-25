// CodeMirror: a small label after each ${VAR} saying what it resolves to from
// the stack's .env -- "= 8181", "= 1000 · default", or "not set". The .env
// was a separate tab, so every ${VAR} had to be looked up by hand, and a
// missing one only showed as a broken container.
import { RangeSetBuilder, StateEffect, StateField } from '@codemirror/state';
import { Decoration, EditorView, ViewPlugin, WidgetType, type DecorationSet, type ViewUpdate } from '@codemirror/view';
import { isSecret, refs, type VarRef } from './composeVars';

/** setEnv hands the editor a new .env (the .env tab changed). */
export const setEnv = StateEffect.define<Record<string, string>>();

const envField = StateField.define<Record<string, string>>({
  create: () => ({}),
  update(v, tr) {
    for (const e of tr.effects) if (e.is(setEnv)) return e.value;
    return v;
  },
});

const MAX = 40;

class VarLabel extends WidgetType {
  constructor(readonly r: VarRef) {
    super();
  }
  eq(o: VarLabel) {
    return o.r.name === this.r.name && o.r.state === this.r.state && o.r.value === this.r.value;
  }
  toDOM() {
    const { name, state, value } = this.r;
    const el = document.createElement('span');
    el.className = `cm-var cm-var-${state}`;
    el.dataset.name = name;
    const shown = isSecret(name) && value ? '••••••' : value.length > MAX ? value.slice(0, MAX) + '…' : value;
    el.textContent =
      state === 'unset' ? 'not set' : state === 'empty' ? 'empty' : state === 'default' ? `= ${shown} · default` : `= ${shown}`;
    el.title =
      state === 'unset'
        ? `${name} is not set in .env and has no default`
        : state === 'default'
          ? `${name} is not set in .env; the default is used`
          : `${name} from .env`;
    return el;
  }
  ignoreEvent() {
    return false;
  }
}

function build(view: EditorView): DecorationSet {
  const b = new RangeSetBuilder<Decoration>();
  for (const r of refs(view.state.doc.toString(), view.state.field(envField))) {
    b.add(r.to, r.to, Decoration.widget({ widget: new VarLabel(r), side: 1 }));
  }
  return b.finish();
}

const labels = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = build(view);
    }
    update(u: ViewUpdate) {
      if (u.docChanged || u.transactions.some((t) => t.effects.some((e) => e.is(setEnv)))) this.decorations = build(u.view);
    }
  },
  { decorations: (v) => v.decorations },
);

const theme = EditorView.baseTheme({
  '.cm-var': {
    marginLeft: '6px',
    padding: '0 5px',
    borderRadius: '4px',
    fontSize: '11px',
    fontFamily: 'ui-sans-serif, system-ui, sans-serif',
    background: 'rgba(255,255,255,0.06)',
  },
  '.cm-var-set': { color: '#8fc28f' },
  '.cm-var-default': { color: '#8b93a1' },
  '.cm-var-empty': { color: '#8b93a1' },
  '.cm-var-unset': { color: '#e3a857', background: 'rgba(227,168,87,0.12)' },
});

/** varHints: the labels, starting from env. With onClick, a label is a
 *  button: it is handed the variable's name and the label's element. */
export function varHints(env: Record<string, string>, onClick?: (name: string, el: HTMLElement) => void) {
  const ext = [envField.init(() => env), labels, theme];
  if (onClick) {
    ext.push(
      EditorView.domEventHandlers({
        mousedown(e) {
          const el = (e.target as HTMLElement).closest<HTMLElement>('.cm-var');
          if (!el?.dataset.name) return false;
          e.preventDefault(); // keep the cursor where it was
          onClick(el.dataset.name, el);
          return true;
        },
      }),
      EditorView.baseTheme({ '.cm-var': { cursor: 'pointer' }, '.cm-var:hover': { textDecoration: 'underline' } }),
    );
  }
  return ext;
}
