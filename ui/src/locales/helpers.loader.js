// One loader for both sides. fjord's UI never renders on a server, but
// vitest transforms for one, and wuchale's stock server loader (locale from
// runWithLocale) gave every helper message as "" in the unit tests. With
// `loader: 'custom'` in wuchale.config.js this file is used everywhere and
// is not regenerated.
import { registerLoaders } from 'wuchale/load-utils'
import { loadCatalog, loadCount } from './.wuchale/helpers.proxy.js'

const key = 'helpers'

export const getRuntime = registerLoaders(key, loadCatalog, loadCount)
export const getRuntimeRx = getRuntime
