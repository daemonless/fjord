// The helpers' text comes from the catalog: load English before any test, as
// main.ts does before mounting, or every message comes back empty.
import { loadLocale } from 'wuchale/load-utils'
import './locales/helpers.loader.js'

await loadLocale('en')
