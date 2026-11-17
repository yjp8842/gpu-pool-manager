import type { ApiEvent, GPU, Reservation } from '../api/client';
import { eventLabel, eventTone, formatTime, profileLabel, workloadLabel } from '../lib/format';
import ui from '../styles/ui.module.css';
import { Pill } from './Pill';
import { EmptyRow } from './Workloads';

export function Reservations({ reservations }: { reservations: Reservation[] }) {
  return (
    <section aria-label="예약" className={ui.card}>
      <div className={ui.head}>
        <h2 className={ui.h2}>예약</h2>
        <span className={ui.note}>
          예약은 노드 + 프로파일 단위이고, 실제 조각은 대상 Pod가 뜨면 정해집니다
        </span>
      </div>
      <div className={ui.tblbox}>
        <table className={ui.tbl}>
          <thead>
            <tr>
              <th>요청 ID</th>
              <th>노드</th>
              <th>프로파일</th>
              <th>대상</th>
              <th>상태</th>
              <th>확정된 조각</th>
              <th>만료</th>
            </tr>
          </thead>
          <tbody>
            {reservations.length === 0 && <EmptyRow cols={7} text="진행 중인 예약이 없습니다" />}
            {reservations.map((r) => (
              <tr key={r.id}>
                <td className={ui.mono} title={r.id}>
                  {r.requestId ?? r.id.slice(0, 8)}
                </td>
                <td>{r.node}</td>
                <td>{profileLabel(r.profile)}</td>
                <td>{workloadLabel(r.target)}</td>
                <td>
                  <ReservationState r={r} />
                </td>
                <BoundResource r={r} />
                <td className={r.state === 'RESERVED' ? undefined : ui.muted}>
                  {r.state === 'RESERVED' ? formatTime(r.expiresAt) : '—'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

/** 확정된 조각. 확정 전이면 Pod를 기다린다. */
function BoundResource({ r }: { r: Reservation }) {
  if (!r.instance) {
    return <td className={ui.muted}>— (Pod 대기)</td>;
  }
  return (
    <td>
      {r.instance.profile === 'full'
        ? `GPU ${r.instance.gpuIndex} · 전체`
        : `GPU ${r.instance.gpuIndex} · GI ${r.instance.giId}`}
    </td>
  );
}

function ReservationState({ r }: { r: Reservation }) {
  switch (r.state) {
    case 'RESERVED':
      return r.capacityShort ? <Pill tone="red">용량 부족</Pill> : <Pill tone="indigo">예약</Pill>;
    case 'ALLOCATED':
      return <Pill tone="blue">확정</Pill>;
    case 'EXPIRED':
      return <Pill tone="yellow">만료</Pill>;
    case 'CANCELLED':
      return <Pill tone="grey">취소</Pill>;
    default:
      return <Pill tone="grey">반납</Pill>;
  }
}

export function Events({ events, gpus }: { events: ApiEvent[]; gpus: GPU[] }) {
  const gpuIndex = new Map(gpus.map((g) => [g.uuid, g.index]));
  const resource = (e: ApiEvent): string => {
    const idx = e.gpuUuid ? gpuIndex.get(e.gpuUuid) : undefined;
    const gpu = e.gpuUuid ? (idx !== undefined ? `GPU ${idx}` : e.gpuUuid) : null;
    if (e.event === 'MIG_RECONFIGURED') {
      return [e.node, gpu, e.message].filter(Boolean).join(' · ');
    }
    if (gpu && e.profile) {
      return e.profile === 'full' ? `${gpu} · 전체` : `${gpu} · GI ${e.giId} · ${e.profile}`;
    }
    const base = [e.node, e.profile ? profileLabel(e.profile) : null].filter(Boolean).join(' · ');
    return e.event === 'REJECTED' && e.message ? `${base} · ${e.message}` : base || '—';
  };

  return (
    <section aria-label="할당 이력" className={ui.card}>
      <div className={ui.head}>
        <h2 className={ui.h2}>할당 이력</h2>
        <span className={ui.note}>최근 {events.length}건</span>
      </div>
      <div className={ui.tblbox}>
        <table className={ui.tbl}>
          <thead>
            <tr>
              <th>시각</th>
              <th>이벤트</th>
              <th>대상 자원</th>
              <th>대상 워크로드</th>
              <th>요청 ID</th>
            </tr>
          </thead>
          <tbody>
            {events.length === 0 && <EmptyRow cols={5} text="이력이 없습니다" />}
            {events.map((e) => (
              <tr key={e.id}>
                <td title={e.occurredAt}>{formatTime(e.occurredAt)}</td>
                <td>
                  <Pill tone={eventTone(e.event)}>{eventLabel(e.event, e.requestId)}</Pill>
                </td>
                <td>{resource(e)}</td>
                <td className={e.workload ? undefined : ui.muted}>{workloadLabel(e.workload)}</td>
                <td className={e.requestId ? ui.mono : ui.muted}>
                  {e.requestId ?? (e.event === 'ALLOCATED' ? '— (예약 없음)' : '—')}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
