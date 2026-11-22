import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

const target = process.env.AGENTHOLD_API_URL ?? 'http://localhost:8080';

export default defineConfig({
  plugins: [react(), tailwindcss()],
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
});
