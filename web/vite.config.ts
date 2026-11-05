import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
export default defineConfig({
  plugins: [react()],
  server: { host: '127.0.0.1', port: 15173, strictPort: true,
    proxy: { '/api': { target: process.env.CAMPUSGO_WEB_API || 'http://127.0.0.1:19080', changeOrigin: true, ws: true, configure(proxy) { if(process.env.CAMPUSGO_WEB_AUTH_MODE==='jwt')proxy.on('proxyReq',req=>req.setHeader('Host','localhost')); } } } },
});
