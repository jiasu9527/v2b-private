import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig(({ command }) => ({
  plugins: [react()],
  base: command === 'build' ? '/assets/admin-new/' : '/',
  build: {
    outDir: '../public/assets/admin-new',
    emptyOutDir: true,
    assetsDir: '.',
    rollupOptions: {
      output: {
        entryFileNames: 'admin.js',
        chunkFileNames: '[name].js',
        assetFileNames: '[name][extname]'
      }
    }
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/monitor': 'http://127.0.0.1:8080'
    }
  }
}));
