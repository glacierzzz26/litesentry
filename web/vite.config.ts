import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// 后端地址可用 LS_API_PROXY 覆盖（8080 常被其他项目占用，如 steady）
const API_TARGET = process.env.LS_API_PROXY ?? 'http://127.0.0.1:8080';

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    // 本地联调：前端 dev → 后端，转发 /api 与 /docs
    proxy: {
      '/api': API_TARGET,
      '/docs': API_TARGET,
    },
  },
});
