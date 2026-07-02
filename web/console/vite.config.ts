import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The console is a static SPA. In dev we proxy /api -> the gateway so the browser
// never hits CORS and the app can use same-origin relative URLs. In prod the app
// reads VITE_GATEWAY_URL (see src/lib/api.ts) and the gateway CORS allows it.
export default defineConfig(({ mode }) => {
  const gateway = process.env.VITE_GATEWAY_URL || 'http://localhost:8080';
  return {
    plugins: [react()],
    server: {
      port: 5173,
      proxy: {
        '/api': { target: gateway, changeOrigin: true },
      },
    },
    build: {
      outDir: 'dist',
      sourcemap: mode !== 'production',
    },
  };
});
