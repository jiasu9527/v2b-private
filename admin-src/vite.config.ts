import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';

// The Go shell loads admin.js?v=<mtime>. A chunk importing ./admin.js would
// execute a second copy of React and createRoot because module URLs differ.
function singleAdminEntry(): Plugin {
  return {
    name: 'single-admin-entry',
    apply: 'build',
    generateBundle(_options, bundle) {
      const chunks = Object.values(bundle).filter((item) => item.type === 'chunk');
      if (chunks.length !== 1 || chunks[0].fileName !== 'admin.js' || !chunks[0].isEntry ||
          chunks[0].imports.length !== 0 || chunks[0].dynamicImports.length !== 0) {
        this.error('The query-versioned admin shell requires a self-contained admin.js. Update the shell asset-loading contract before enabling code splitting.');
      }
    },
  };
}

export default defineConfig(({ command }) => ({
  plugins: [react(), singleAdminEntry()],
  base: command === 'build' ? '/assets/admin-new/' : '/',
  build: {
    outDir: '../public/assets/admin-new',
    emptyOutDir: true,
    assetsDir: '.',
    rollupOptions: {
      output: {
        entryFileNames: 'admin.js',
        inlineDynamicImports: true,
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
