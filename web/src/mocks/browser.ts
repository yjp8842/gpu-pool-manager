// 브라우저용 MSW (npm run dev:mock). 워커 스크립트는 vite.config.ts의 msw 플러그인이 내준다.
import { setupWorker } from 'msw/browser';
import { handlers } from './handlers';

export const worker = setupWorker(...handlers);
