// 여러 조회 결과를 묶어 화면의 요약 숫자와 노드별 값을 만든다.
import type { Capacity, GPU, Node, Reservation, Workload } from '../api/client';
import { profileLabel, shortModel } from './format';

export interface Summary {
  nodes: number;
  readyNodes: number;
  /** "A100-SXM4-40GB 1" */
  cardsDetail: string;
  cards: number;
  instances: number;
  free: number;
  allocated: number;
  unhealthy: number;
  openReservations: number;
  pods: number;
  runningPods: number;
}

export function summarize(
  nodes: Node[],
  gpus: GPU[],
  workloads: Workload[],
  reservations: Reservation[],
): Summary {
  const byModel = new Map<string, number>();
  for (const g of gpus) {
    const m = shortModel(g.model);
    byModel.set(m, (byModel.get(m) ?? 0) + 1);
  }

  const instances = gpus.flatMap((g) => g.instances);
  return {
    nodes: nodes.length,
    readyNodes: nodes.filter((n) => n.ready === true).length,
    cards: gpus.length,
    cardsDetail: [...byModel].map(([m, c]) => `${m} ${c}`).join(' · ') || '없음',
    instances: instances.length,
    free: instances.filter((i) => i.state === 'FREE').length,
    allocated: instances.filter((i) => i.state === 'ALLOCATED').length,
    unhealthy: instances.filter((i) => i.state === 'UNHEALTHY').length,
    openReservations: reservations.filter((r) => r.state === 'RESERVED').length,
    pods: workloads.length,
    runningPods: workloads.filter((w) => w.phase === 'Running').length,
  };
}

/** 노드의 남은 수량 요약: "1g.5gb 2". */
export function availableLabel(node: Node, capacity: Capacity[]): string {
  const rows = capacity.filter((c) => c.node === node.name);
  if (rows.length === 0) {
    return '—';
  }
  return rows.map((c) => `${profileLabel(c.profile)} ${c.available}`).join(' · ');
}

/** 노드의 GPU 카드: "A100-SXM4-40GB × 1". */
export function cardsLabel(node: Node, gpus: GPU[]): string {
  const mine = gpus.filter((g) => g.node === node.name);
  if (mine.length === 0) {
    return '—';
  }
  const byModel = new Map<string, number>();
  for (const g of mine) {
    const m = shortModel(g.model);
    byModel.set(m, (byModel.get(m) ?? 0) + 1);
  }
  return [...byModel].map(([m, c]) => `${m} × ${c}`).join(' · ');
}

/** MIG 켜진 카드 수: "1 / 8 켜짐". 카드 정보가 없으면 null. */
export function migLabel(node: Node, gpus: GPU[]): string | null {
  const mine = gpus.filter((g) => g.node === node.name);
  if (mine.length === 0) {
    return null;
  }
  return `${mine.filter((g) => g.migEnabled === true).length} / ${mine.length} 켜짐`;
}
