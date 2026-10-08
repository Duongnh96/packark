import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  build: {
    // The Go binary embeds this directory.
    outDir: '../backend/internal/api/dist',
    // Off so the committed .gitkeep survives a build.
    emptyOutDir: false,
  },
  server: {
    proxy: { '/api': 'http://localhost:8080' },
  },
})
