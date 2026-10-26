import { mount } from 'svelte'
import { loadLocale } from 'wuchale/load-utils'
import './locales/main.loader.svelte.js'
import './locales/helpers.loader.js'
import './app.css'
import Root from './Root.svelte'
import { guardFetch } from './auth'
import { currentLocale } from './locale'

guardFetch()

// The catalog for the chosen language is loaded before anything mounts, so
// every component renders in it from the first paint.
const locale = currentLocale()
document.documentElement.lang = locale
await loadLocale(locale)

const app = mount(Root, {
  target: document.getElementById('app')!,
})

export default app
