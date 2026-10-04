<script lang="ts">
  import { onMount, onDestroy, createEventDispatcher } from 'svelte';
  import { basicSetup, EditorView } from 'codemirror';
  import { yaml } from '@codemirror/lang-yaml';
  import { oneDark } from '@codemirror/theme-one-dark';
  import { EditorState, Compartment } from '@codemirror/state';
  import { keymap } from '@codemirror/view';
  import { varHints, setEnv } from './varHints';
  import { pageIsLight, onThemeChange } from './theme';

  export let content = '';
  export let language: 'yaml' | 'env' = 'yaml';
  export let readonly = false;
  // The stack's .env, parsed: set, each ${VAR} gets a label with its value.
  export let vars: Record<string, string> | null = null;
  // Labels are clickable when set: 'varclick' carries the name and where the
  // label is on screen.
  export let varsClickable = false;

  const dispatch = createEventDispatcher();
  let editorContainer: HTMLDivElement;
  let view: EditorView;

  // The page's theme, swapped in place through a compartment: rebuilding the
  // editor would drop the cursor and the undo history.
  const themeSlot = new Compartment();
  // Light is CodeMirror's own default look (basicSetup highlights for it);
  // only the background is set, to sit on the card it is drawn in.
  const editorTheme = () =>
    pageIsLight()
      ? EditorView.theme({ '.cm-scroller': { background: 'var(--color-fjord-card)' } })
      : [oneDark, EditorView.theme({ '.cm-scroller': { background: '#161619' } }, { dark: true })];
  let unwatchTheme: () => void;

  function initEditor() {
    if (view) view.destroy();
    if (!editorContainer) return;

    const saveKeymap = keymap.of([
      {
        key: 'Mod-s',
        run: () => {
          dispatch('save');
          return true;
        },
      },
    ]);

    const extensions = [
      basicSetup,
      themeSlot.of(editorTheme()),
      saveKeymap,
      EditorView.updateListener.of((update) => {
        if (update.docChanged) {
          content = view.state.doc.toString();
          dispatch('change', content);
        }
      }),
    ];

    if (language === 'yaml') {
      extensions.push(yaml());
    }
    if (vars)
      extensions.push(
        varHints(vars, varsClickable ? (name, el) => dispatch('varclick', { name, rect: el.getBoundingClientRect() }) : undefined),
      );

    if (readonly) {
      // A generated spec (e.g. appjail-director.yml) is shown for reference,
      // not edited: fjord regenerates it, so hand-edits would be lost.
      extensions.push(EditorState.readOnly.of(true), EditorView.editable.of(false));
    }

    const state = EditorState.create({
      doc: content,
      extensions,
    });

    view = new EditorView({
      state,
      parent: editorContainer,
    });
  }

  $: if (editorContainer) {
    if (!view || view.state.doc.toString() !== content) {
      initEditor();
    }
  }

  // A new .env relabels in place; rebuilding the editor would lose the
  // cursor and the undo history.
  $: if (view && vars) view.dispatch({ effects: setEnv.of(vars) });

  onMount(() => {
    initEditor();
    unwatchTheme = onThemeChange(() => {
      if (view) view.dispatch({ effects: themeSlot.reconfigure(editorTheme()) });
    });
  });

  onDestroy(() => {
    unwatchTheme?.();
    if (view) view.destroy();
  });
</script>

<div bind:this={editorContainer} class="h-full w-full overflow-hidden text-left rounded-b-xl border-t-0 border-fjord-border shadow-inner"></div>

<style>
  :global(.cm-editor) {
    height: 100%;
    outline: none !important;
  }
  :global(.cm-scroller) {
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
    font-size: 14px;
  }
</style>
