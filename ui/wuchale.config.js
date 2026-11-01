// @ts-check
import { adapter as svelte, defaultArgs, svelteDefaultHeuristic } from '@wuchale/svelte'
import { adapter as vanilla } from 'wuchale/adapter-vanilla'
import { defineConfig, defaultHeuristic, pofile } from 'wuchale'

// Strings the code compares or sends, not text: a translation would break
// them (e.key === 'Escape', CodeMirror's 'Mod-s', a header name).
const notText = new Set([
  'ArrowDown', 'ArrowLeft', 'ArrowRight', 'ArrowUp', 'Enter', 'Escape',
  'AbortError', 'Mod-s', 'X-Fjord-Stack-Id',
  'Esc', 'Ctrl F', // key names the shortcut table matches on
])
/** @param {(txt: any, file: string) => any} base */
const skipCode = (base) => (txt, file) => {
  const body = typeof txt.body === 'string' ? txt.body : txt.body.join('')
  if (notText.has(body) || body.startsWith('OPTION ')) return false // a Makejail's text
  // A class list built with a template (`${base} flex-1`): utility classes, not text.
  if (/^(\{\d\} )?[a-z0-9:-]+( [a-z0-9:-]+)*$/.test(body) && /(^| )(flex|flex-1|mb-\d|max-w-\d+|px-\d|font-mono|uppercase|items-center|justify-center)( |$)/.test(body)) return false
  const res = base(txt, file)
  if (res || !/\p{L}/u.test(body)) return res
  const path = txt.path
  // Shown text in a markup expression: {on ? 'change' : '+ add'}. Only with a
  // space -- a bare word there is a value the code compares ('host').
  if (path.at(-1)?.type === 'expression' && path.some((s) => s.type === 'element') &&
      !path.some((s) => s.type === 'attribute') && /\s/.test(body.trim())) return 'message'
  // A legacy `$: rows = [{ label: 'Platform' }]`: a top-level reactive
  // assignment, which wuchale takes for a bare statement. Capitalised only,
  // as wuchale does inside functions.
  if (path[0]?.type === 'expression' && path[1]?.type === 'assignment' &&
      /^\p{Lu}/u.test(body) && !/^([A-Z]|\P{L})+$/u.test(body)) return 'message'
  return res
}

export default defineConfig({
  locales: ['en', 'fr'],
  adapters: {
    main: svelte({
      loader: 'svelte',
      heuristic: skipCode(svelteDefaultHeuristic),
      // The components are legacy Svelte (`$:`), and a `$derived` wrap
      // forces runes mode. The locale is loaded before mount, so plain is enough.
      runtime: { ...defaultArgs.runtime, initReactive: () => false },
    }),
    helpers: vanilla({
      loader: 'custom', // src/locales/helpers.loader.js, both sides
      files: ['src/updateWords.ts', 'src/publicUrl.ts', 'src/folderMatch.ts', 'src/choices.ts', 'src/appUrl.ts', 'src/restartPolicy.ts', 'src/stackHealth.ts', 'src/names.ts', 'src/shortcuts.ts', 'src/password.ts', 'src/busyStep.ts'],
      heuristic: skipCode(defaultHeuristic),
      // A catalog of its own: shared with main, a message's index depends on
      // the .svelte files having been seen first, and the unit tests (which
      // load these alone) got the wrong entries.
      storage: pofile({ location: 'src/locales/helpers/{locale}.po' }),
    }),
  },
})
