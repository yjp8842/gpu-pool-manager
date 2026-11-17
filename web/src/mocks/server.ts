// 테스트용 MSW (Node). src/test/setup.ts가 테스트마다 켜고 끈다.
import { setupServer } from 'msw/node';
import { handlers } from './handlers';

export const server = setupServer(...handlers);
