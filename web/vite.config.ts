import { fileURLToPath } from 'node:url';
import { mkdirSync, writeFileSync } from 'node:fs';
import type { Plugin } from 'vite';
import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// Vite empties dist/ on build; recreate the placeholder so Go's
// `//go:embed all:dist` always has a file to embed.
function keepDist(): Plugin {
  return {
    name: 'agenthold-keep-dist',
    apply: 'build',
    closeBundle() {
      mkdirSync('dist', { recursive: true });
      writeFileSync('dist/.gitkeep', '');
    },
  };
}

const target = process.env.AGENTHOLD_API_URL ?? 'http://localhost:8080';

export default defineConfig({
  plugins: [react(), tailwindcss(), keepDist()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  // Scan every source file up front, so lazily imported chunks never make the
  // dev server discover new dependencies and reload the page mid-session.
  optimizeDeps: {
    entries: ['index.html', 'src/**/*.{ts,tsx}', '!src/**/*.test.{ts,tsx}'],
  },
  server: {
    host: true,
    port: 5173,
    proxy: {
      '/api': { target, ws: true, changeOrigin: true },
      '/healthz': { target, ws: true, changeOrigin: true },
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test-setup.ts'],
    exclude: ['e2e/**', 'node_modules/**'],
  },
});
