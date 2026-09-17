import { defineConfig } from 'vite';
import { resolve } from 'node:path';

export default defineConfig({
  build: {
    manifest: true,
    rollupOptions: {
      input: {
        desktop: resolve(import.meta.dirname, 'index.html'),
        phone: resolve(import.meta.dirname, 'phone.html'),
        secure: resolve(import.meta.dirname, 'secure.html'),
      },
    },
  },
});
