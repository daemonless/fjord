import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    tailwindcss(),
    svelte()
  ],
  server: {
    allowedHosts: true,
    proxy: {
      // changeOrigin must stay false: fjordd refuses cross-origin mutations by
      // comparing the browser's Origin against the request Host (see
      // cmd/fjordd/security.go). Rewriting Host to the target makes every
      // POST/DELETE from the dev server a 403 "cross-origin request refused",
      // while GETs still work -- so reads look fine and writes mysteriously fail.
      // ws:true also carries the shell's WebSocket upgrade, which lives under
      // /api (/api/stacks/<name>/exec), not a separate route.
      '/api': { target: 'http://localhost:3567', changeOrigin: false, ws: true }
    }
  }
})
