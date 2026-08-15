import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  base: '/model-manager/static/vue/',
  build: {
    outDir: '../static/vue',
    emptyOutDir: true,
    sourcemap: true,
  },
  server: {
    port: 4174,
    proxy: {
      '/model-manager/api': {
        target: 'http://10.1.1.4:8081',
        changeOrigin: true,
      },
    },
  },
})
