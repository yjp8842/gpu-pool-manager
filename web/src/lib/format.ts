// 화면에 보이는 글자를 만드는 함수들. API 값은 장부와 같은 대문자 값이라 여기서 한국어로 바꾼다.
import type { EventType, Instance, InstanceRef, WorkloadRef } from '../api/client';

export const PROFILE_FULL = 'full';

/** 시각을 브라우저 시간대의 HH:MM:SS로 보여준다. 값이 없으면 —. */
export function formatTime(value: string | number | null | undefined): string {
  if (value === null || value === undefined || value === '') {
    return '—';
  }
  const d = new Date(value);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** 프로파일 이름. 나누지 않은 GPU(full)는 "GPU 전체". */
export function profileLabel(profile: string): string {
  return profile === PROFILE_FULL ? 'GPU 전체' : profile;
}

/**
 * 조각이 차지하는 연산 슬라이스 수 (막대에서 칸 너비). "1g.5gb" → 1, "3g.20gb" → 3.
 * 나누지 않은 GPU는 카드 전체(totalSlices, 모르면 1)다.
 */
export function profileSlices(profile: string, totalSlices?: number | null): number {
  if (profile === PROFILE_FULL) {
    return totalSlices ?? 1;
  }
  const m = /^(\d+)g\./.exec(profile);
  return m ? Number(m[1]) : 1;
}

/** 카드 제목용 모델 이름: "NVIDIA RTX PRO 6000 Blackwell Server Edition" → "NVIDIA RTX PRO 6000 Blackwell". */
export function displayModel(model: string): string {
  return model.replace(/\s+Server Edition$/, '');
}

/** 표·요약용 짧은 모델 이름: "NVIDIA A100-SXM4-40GB" → "A100-SXM4-40GB". */
export function shortModel(model: string): string {
  return displayModel(model).replace(/^NVIDIA\s+/, '');
}

/** 워크로드 이름: "Pod research/notebook-c". */
export function workloadLabel(w: WorkloadRef | null | undefined): string {
  if (!w) {
    return '—';
  }
  return `Pod ${w.namespace}/${w.name}`;
}

/** 조각 하나: "GPU 0 · GI 7 · 1g.5gb", 나누지 않은 GPU는 "GPU 1 · 전체". */
export function instanceLabel(ref: Pick<InstanceRef, 'gpuIndex' | 'giId' | 'profile'>): string {
  if (ref.profile === PROFILE_FULL) {
    return `GPU ${ref.gpuIndex} · 전체`;
  }
  return `GPU ${ref.gpuIndex} · GI ${ref.giId} · ${ref.profile}`;
}

/** 워크로드가 쓰는 조각 목록. 나누지 않은 GPU 여러 장은 "GPU 1, GPU 2 · 전체"로 묶는다. */
export function instancesLabel(refs: InstanceRef[]): string {
  if (refs.length === 0) {
    return '—';
  }
  if (refs.every((r) => r.profile === PROFILE_FULL)) {
    return `${refs.map((r) => `GPU ${r.gpuIndex}`).join(', ')} · 전체`;
  }
  return refs.map(instanceLabel).join(', ');
}

/** 조각을 쓰는 주체 이름: 묶인 할당의 대상, 없으면 DCGM이 관측한 Pod. */
export function instanceUser(in_: Instance): string | null {
  return in_.allocation?.target.name ?? in_.observedPod?.name ?? null;
}

export type Tone = 'green' | 'red' | 'yellow' | 'grey' | 'indigo' | 'blue';

/** Pod phase 색. 삭제 중(Terminating)은 서버1이 붙인 값이다. */
export function phaseTone(phase: string): Tone {
  switch (phase) {
    case 'Running':
      return 'green';
    case 'Pending':
    case 'Terminating':
      return 'yellow';
    case 'Failed':
      return 'red';
    default:
      return 'grey';
  }
}

const eventNames: Record<EventType, string> = {
  RESERVED: '예약',
  ALLOCATED: '할당',
  RELEASED: '반납',
  EXPIRED: '예약 만료',
  CANCELLED: '취소',
  REJECTED: '거부 · 409',
  MIG_RECONFIGURED: '구성 변경',
};

const eventTones: Record<EventType, Tone> = {
  RESERVED: 'indigo',
  ALLOCATED: 'blue',
  RELEASED: 'grey',
  EXPIRED: 'yellow',
  CANCELLED: 'grey',
  REJECTED: 'red',
  MIG_RECONFIGURED: 'grey',
};

/** 이력 이벤트 이름. 할당은 예약을 거쳤는지(요청 ID)로 "조각 확정"과 "관측"을 나눈다. */
export function eventLabel(event: EventType, requestId: string | null): string {
  if (event === 'ALLOCATED') {
    return requestId ? '할당 · 조각 확정' : '할당 · 관측';
  }
  return eventNames[event] ?? event;
}

export function eventTone(event: EventType): Tone {
  return eventTones[event] ?? 'grey';
}
