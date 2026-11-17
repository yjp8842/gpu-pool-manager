// 서버2 API 클라이언트. 타입은 api/openapi.yaml에서 생성한 schema.d.ts를 쓴다 (npm run gen:api).
import createClient from 'openapi-fetch';
import type { components, paths } from './schema';

export type Schemas = components['schemas'];
export type Node = Schemas['Node'];
export type GPU = Schemas['GPU'];
export type Instance = Schemas['Instance'];
export type InstanceRef = Schemas['InstanceRef'];
export type Capacity = Schemas['Capacity'];
export type Workload = Schemas['Workload'];
export type WorkloadRef = Schemas['WorkloadRef'];
export type Reservation = Schemas['Reservation'];
export type ApiEvent = Schemas['Event'];
export type EventType = Schemas['EventType'];

// 화면은 서버2와 같은 출처에서 열린다. 개발 서버(vite)는 /api를 서버2로 넘긴다.
// fetch는 요청할 때마다 globalThis에서 찾는다. 테스트의 MSW가 나중에 fetch를 바꿔 끼워도 그것을 쓰게 하기 위해서다.
export const api = createClient<paths>({
  baseUrl: window.location.origin,
  fetch: (request) => globalThis.fetch(request),
});

/** 서버2가 오류(4xx·5xx)를 돌려주면 던지는 오류다. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

type Result<T> = { data?: T; error?: unknown; response: Response };

/** openapi-fetch 결과에서 본문을 꺼낸다. 오류 응답이면 ApiError를 던진다. */
export async function unwrap<T>(call: Promise<Result<T>>): Promise<T> {
  const { data, error, response } = await call;
  if (data !== undefined) {
    return data;
  }
  const e = (error ?? {}) as Partial<Schemas['Error']>;
  throw new ApiError(response.status, e.code ?? 'unknown', e.message ?? response.statusText);
}
