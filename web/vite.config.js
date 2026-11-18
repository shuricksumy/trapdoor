import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The build goes into the Go package that embeds it (internal/webui/dist).
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    assetsInlineLimit: 0, // keep CSP simple: no data: URLs for fonts
  },
  server: {
    // npm run dev: talk to a local `trapdoor web` on :8080
    proxy: { '/api': 'http://127.0.0.1:8080' },
  },
});
