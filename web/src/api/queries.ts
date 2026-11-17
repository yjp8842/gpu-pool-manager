// 화면이 쓰는 조회. 모두 30초마다 다시 불러온다 (설계도 8장).
import { useQuery } from '@tanstack/react-query';
import { api, unwrap } from './client';

export const REFRESH_INTERVAL_MS = 30_000;
export const EVENT_LIMIT = 20;

const options = { refetchInterval: REFRESH_INTERVAL_MS } as const;

export function useNodes() {
  return useQuery({
    ...options,
    queryKey: ['nodes'],
    queryFn: () => unwrap(api.GET('/api/v1/nodes')),
  });
}

export function useGPUs() {
  return useQuery({
    ...options,
    queryKey: ['gpus'],
    queryFn: () => unwrap(api.GET('/api/v1/gpus')),
  });
}

export function useCapacity() {
  return useQuery({
    ...options,
    queryKey: ['capacity'],
    queryFn: () => unwrap(api.GET('/api/v1/capacity')),
  });
}

export function useWorkloads() {
  return useQuery({
    ...options,
    queryKey: ['workloads'],
    queryFn: () => unwrap(api.GET('/api/v1/workloads')),
  });
}

export function useReservations() {
  return useQuery({
    ...options,
    queryKey: ['reservations'],
    queryFn: () => unwrap(api.GET('/api/v1/reservations')),
  });
}

export function useEvents() {
  return useQuery({
    ...options,
    queryKey: ['events', EVENT_LIMIT],
    queryFn: () => unwrap(api.GET('/api/v1/events', { params: { query: { limit: EVENT_LIMIT } } })),
  });
}
