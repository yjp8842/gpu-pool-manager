// MSW 요청 처리기. 서버2 없이 화면을 개발하고 테스트할 때 쓴다 (npm run dev:mock, 화면 테스트).
// 조회 조건(node, profile, state, limit)도 서버2와 같은 뜻으로 거른다.
import { http, HttpResponse } from 'msw';
import type { Schemas } from '../api/client';
import * as data from './fixtures';

const param = (request: Request, name: string) => new URL(request.url).searchParams.get(name);

export const handlers = [
  http.get('/healthz', () => HttpResponse.json(data.health)),

  http.get('/api/v1/nodes', () => HttpResponse.json<Schemas['NodeList']>({ items: data.nodes })),

  http.get('/api/v1/gpus', ({ request }) => {
    const node = param(request, 'node');
    return HttpResponse.json<Schemas['GPUList']>({
      items: data.gpus.filter((g) => !node || g.node === node),
    });
  }),

  http.get('/api/v1/instances', ({ request }) => {
    const [node, profile, state] = ['node', 'profile', 'state'].map((n) => param(request, n));
    const items = data.gpus
      .flatMap((g) => g.instances)
      .filter(
        (i) =>
          (!node || i.node === node) &&
          (!profile || i.profile === profile) &&
          (!state || i.state === state),
      );
    return HttpResponse.json<Schemas['InstanceList']>({ items });
  }),

  http.get('/api/v1/capacity', ({ request }) => {
    const [node, profile] = ['node', 'profile'].map((n) => param(request, n));
    const items = data.capacity.filter(
      (c) => (!node || c.node === node) && (!profile || c.profile === profile),
    );
    return HttpResponse.json<Schemas['CapacityList']>({ items });
  }),

  http.get('/api/v1/workloads', () =>
    HttpResponse.json<Schemas['WorkloadList']>({ items: data.workloads }),
  ),

  http.get('/api/v1/reservations', ({ request }) => {
    const state = param(request, 'state');
    const items = data.reservations.filter((r) =>
      state ? r.state === state : r.state === 'RESERVED' || r.state === 'ALLOCATED',
    );
    return HttpResponse.json<Schemas['ReservationList']>({ items });
  }),

  http.get('/api/v1/reservations/:id', ({ params }) => {
    const r = data.reservations.find((x) => x.id === params.id);
    if (!r) {
      return HttpResponse.json<Schemas['Reservation'] | Schemas['Error']>(
        { code: 'not_found', message: 'reservation not found' },
        { status: 404 },
      );
    }
    return HttpResponse.json<Schemas['Reservation'] | Schemas['Error']>(r);
  }),

  http.get('/api/v1/events', ({ request }) => {
    const limit = Number(param(request, 'limit') ?? 100);
    return HttpResponse.json<Schemas['EventList']>({
      items: data.events.slice(0, limit),
      nextBefore: null,
    });
  }),
];
