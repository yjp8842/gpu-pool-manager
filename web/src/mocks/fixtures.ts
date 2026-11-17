// 목업 API 응답 (화면 개발용). 설계도의 데모 구성을 흉내 낸다: GKE MIG 노드 풀의 노드 한 대
// (a2-highgpu-1g, A100 1장)를 1g.5gb × 7로 나눈다. 카드·조각·예약만 손으로 적고, 노드 요약·남은 수량·워크로드가 쓰는 조각은 그것에서 계산해
// 서버2 응답처럼 서로 맞게 만든다. 타입은 API 명세에서 생성한 것이라 명세와 모양이 다르면 컴파일되지 않는다.
import type {
  ApiEvent,
  Capacity,
  GPU,
  Instance,
  InstanceRef,
  Node,
  Reservation,
  Schemas,
  Workload,
} from '../api/client';

const now = Date.now();
const minutesFromNow = (m: number) => new Date(now + m * 60_000).toISOString();

const MIG_NODE = 'gpu-mig-1'; // a2-highgpu-1g, MIG 1g.5gb × 7

const RSV_WAITING = '0f3b6a2e-1c4d-4e8f-9a7b-000000000042';
const RSV_BOUND = '0f3b6a2e-1c4d-4e8f-9a7b-000000000041';

let nextInstanceID = 1;

interface Use {
  pod: [namespace: string, name: string];
  /** 예약 ID가 있으면 예약을 거친 할당, 없으면 관측 할당 */
  reservation?: string;
}

function instance(
  gpu: GPU,
  giId: number,
  profile: string,
  memoryMb: number,
  use?: Use,
  health: Instance['health'] = 'OK',
): Instance {
  const id = nextInstanceID++;
  const allocation = use
    ? {
        id: use.reservation ?? `0f3b6a2e-1c4d-4e8f-9a7b-${String(id).padStart(12, '0')}`,
        source: use.reservation ? ('RESERVATION' as const) : ('OBSERVED' as const),
        target: { kind: 'POD' as const, namespace: use.pod[0], name: use.pod[1] },
        allocatedAt: minutesFromNow(-30),
      }
    : null;
  return {
    id,
    node: gpu.node,
    gpuUuid: gpu.uuid,
    gpuIndex: gpu.index,
    giId,
    profile,
    memoryMb,
    migUuid: profile === 'full' ? null : `MIG-${gpu.uuid.slice(4, 12)}-${giId}`,
    health,
    state: allocation ? 'ALLOCATED' : health === 'OK' ? 'FREE' : 'UNHEALTHY',
    observedPod: use ? { namespace: use.pod[0], name: use.pod[1], container: 'main' } : null,
    allocation,
    lastSeenAt: minutesFromNow(0),
    missedCount: 0,
  };
}

function gpu(
  node: string,
  index: number,
  model: string,
  memoryMb: number,
  migEnabled: boolean,
  totalSlices: number,
): GPU {
  return {
    uuid: `GPU-${node.slice(0, 3)}${index}c1e2b-90aa-4f6e-9b31-0c2d8e1f4a0${index}`,
    node,
    index,
    model,
    memoryMb,
    migEnabled,
    totalSlices,
    lastXid: 0,
    lastSeenAt: minutesFromNow(0),
    missedCount: 0,
    instances: [],
  };
}

// ---- 카드와 조각 ----

const A100 = 'NVIDIA A100-SXM4-40GB';
const A100_MEMORY_MB = 40960;

const mig = gpu(MIG_NODE, 0, A100, A100_MEMORY_MB, true, 7);
// 데모 분할: 1g.5gb × 7
mig.instances = [
  instance(mig, 7, '1g.5gb', 5120, { pod: ['grim', 'infer-a'] }),
  instance(mig, 8, '1g.5gb', 5120, { pod: ['grim', 'infer-b'] }),
  instance(mig, 9, '1g.5gb', 5120, { pod: ['grim', 'infer-c'], reservation: RSV_BOUND }),
  instance(mig, 10, '1g.5gb', 5120),
  instance(mig, 11, '1g.5gb', 5120),
  instance(mig, 12, '1g.5gb', 5120),
  instance(mig, 13, '1g.5gb', 5120, undefined, 'UNHEALTHY'),
];

export const gpus: GPU[] = [mig];
const allInstances = gpus.flatMap((g) => g.instances);

// GKE MIG 노드 풀 방식 (설계도 2장): 조각도 나누지 않은 GPU도 자원 이름은 nvidia.com/gpu이고,
// 조각 크기는 노드 라벨로 고른다.
const RESOURCE_NAME = 'nvidia.com/gpu';
function nodeSelector(profile: string): Record<string, string> {
  return profile === 'full' ? {} : { 'cloud.google.com/gke-gpu-partition-size': profile };
}

// ---- 예약 ----

const bound = allInstances.find((i) => i.allocation?.id === RSV_BOUND)!;
export const reservations: Reservation[] = [
  {
    id: RSV_WAITING,
    source: 'RESERVATION',
    state: 'RESERVED',
    requestId: 'rsv-0042',
    requester: 'research-team',
    node: MIG_NODE,
    profile: '1g.5gb',
    resourceName: RESOURCE_NAME,
    nodeSelector: nodeSelector('1g.5gb'),
    target: { kind: 'POD', namespace: 'research', name: 'notebook-c' },
    instance: null,
    reservedAt: minutesFromNow(-1),
    expiresAt: minutesFromNow(4),
    allocatedAt: null,
    endedAt: null,
    capacityShort: false,
  },
  {
    id: RSV_BOUND,
    source: 'RESERVATION',
    state: 'ALLOCATED',
    requestId: 'rsv-0041',
    requester: 'grim-serving',
    node: MIG_NODE,
    profile: '1g.5gb',
    resourceName: RESOURCE_NAME,
    nodeSelector: nodeSelector('1g.5gb'),
    target: { kind: 'POD', namespace: 'grim', name: 'infer-c' },
    instance: ref(bound),
    reservedAt: minutesFromNow(-9),
    expiresAt: minutesFromNow(-4),
    allocatedAt: minutesFromNow(-8),
    endedAt: null,
    capacityShort: false,
  },
];

function ref(i: Instance): InstanceRef {
  return { id: i.id, gpuUuid: i.gpuUuid, gpuIndex: i.gpuIndex, giId: i.giId, profile: i.profile };
}

// ---- 계산되는 값: 남은 수량, 노드 요약, 워크로드 ----

export const capacity: Capacity[] = (() => {
  const rows = new Map<string, Capacity>();
  for (const i of allInstances) {
    const key = `${i.node}/${i.profile}`;
    const row = rows.get(key) ?? {
      node: i.node,
      profile: i.profile,
      resourceName: RESOURCE_NAME,
      nodeSelector: nodeSelector(i.profile),
      total: 0,
      free: 0,
      reserved: 0,
      available: 0,
    };
    row.total++;
    if (i.state === 'FREE') row.free++;
    rows.set(key, row);
  }
  for (const r of reservations) {
    const row = rows.get(`${r.node}/${r.profile}`);
    if (row && r.state === 'RESERVED') row.reserved++;
  }
  for (const row of rows.values()) row.available = Math.max(row.free - row.reserved, 0);
  return [...rows.values()].sort((a, b) =>
    `${a.node}/${a.profile}`.localeCompare(`${b.node}/${b.profile}`),
  );
})();

function nodeSummary(name: string, instanceType: string): Node {
  const mine = gpus.filter((g) => g.node === name);
  const inst = mine.flatMap((g) => g.instances);
  return {
    name,
    ready: true,
    instanceType,
    lastSeenAt: minutesFromNow(0),
    gpuCount: mine.length,
    instanceCount: inst.length,
    freeInstanceCount: inst.filter((i) => i.state === 'FREE').length,
  };
}

function pod(namespace: string, name: string, node: string | null, phase = 'Running'): Workload {
  const used = allInstances.filter(
    (i) =>
      (i.observedPod?.namespace === namespace && i.observedPod.name === name) ||
      (i.allocation?.target.namespace === namespace && i.allocation.target.name === name),
  );
  return {
    kind: 'POD',
    namespace,
    name,
    uid: `uid-${name}`,
    node,
    phase,
    instances: used.map(ref),
    lastSeenAt: minutesFromNow(0),
  };
}

export const workloads: Workload[] = [
  pod('grim', 'infer-a', MIG_NODE),
  pod('grim', 'infer-b', MIG_NODE),
  pod('grim', 'infer-c', MIG_NODE),
  pod('research', 'notebook-c', null, 'Pending'),
];

export const nodes: Node[] = [nodeSummary(MIG_NODE, 'a2-highgpu-1g')];

// ---- 이력 (최근 것부터) ----

function event(
  id: number,
  minutesAgo: number,
  e: Partial<ApiEvent> & Pick<ApiEvent, 'event'>,
): ApiEvent {
  return {
    id,
    occurredAt: minutesFromNow(-minutesAgo),
    node: null,
    gpuUuid: null,
    giId: null,
    profile: null,
    migUuid: null,
    workload: null,
    requester: null,
    requestId: null,
    allocationId: null,
    message: null,
    ...e,
  };
}

const gpu0 = mig.uuid;
export const events: ApiEvent[] = [
  event(6, 1, {
    event: 'RESERVED',
    node: MIG_NODE,
    profile: '1g.5gb',
    requestId: 'rsv-0042',
    requester: 'research-team',
    allocationId: RSV_WAITING,
    workload: { kind: 'POD', namespace: 'research', name: 'notebook-c' },
  }),
  event(5, 8, {
    event: 'ALLOCATED',
    node: MIG_NODE,
    gpuUuid: gpu0,
    giId: 9,
    profile: '1g.5gb',
    migUuid: bound.migUuid,
    requestId: 'rsv-0041',
    allocationId: RSV_BOUND,
    message: '예약 확정',
    workload: { kind: 'POD', namespace: 'grim', name: 'infer-c' },
  }),
  event(4, 33, {
    event: 'RELEASED',
    node: MIG_NODE,
    gpuUuid: gpu0,
    giId: 10,
    profile: '1g.5gb',
    requestId: 'rsv-0035',
    message: '대상 Pod가 조각에서 사라짐',
    workload: { kind: 'POD', namespace: 'grim', name: 'infer-old' },
  }),
  event(3, 180, {
    event: 'REJECTED',
    node: MIG_NODE,
    profile: '1g.5gb',
    requestId: 'rsv-0030',
    message: '남은 수량 0',
    workload: { kind: 'POD', namespace: 'research', name: 'batch-9' },
  }),
  event(2, 230, {
    event: 'ALLOCATED',
    node: MIG_NODE,
    gpuUuid: gpu0,
    giId: 7,
    profile: '1g.5gb',
    migUuid: mig.instances[0].migUuid,
    message: '관측 할당 (예약 없음)',
    workload: { kind: 'POD', namespace: 'grim', name: 'infer-a' },
  }),
  event(1, 300, {
    event: 'MIG_RECONFIGURED',
    node: MIG_NODE,
    gpuUuid: gpu0,
    message: '나누지 않음 → 1g.5gb×7',
  }),
];

export const health: Schemas['Health'] = { status: 'ok' };
