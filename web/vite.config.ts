import react from '@vitejs/plugin-react';
import { msw } from 'msw/vite';
import { defineConfig } from 'vitest/config';

// 개발 서버는 /api와 /healthz를 서버2(기본 :8080)로 넘긴다.
// npm run dev:mock이면 서버2 대신 가짜 API(MSW)가 응답한다. msw 플러그인은 개발 서버에서 워커 스크립트만 내주고,
// 배포용 빌드에는 넣지 않는다.
export default defineConfig(({ command }) => ({
  plugins: [react(), ...(command === 'serve' ? [msw({ mode: 'worker-only' })] : [])],
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/healthz': 'http://localhost:8080',
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
  },
}));
