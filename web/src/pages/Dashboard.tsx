import { useQueryClient } from '@tanstack/react-query';
import {
  useCapacity,
  useEvents,
  useGPUs,
  useNodes,
  useReservations,
  useWorkloads,
  REFRESH_INTERVAL_MS,
} from '../api/queries';
import { GPUPartitions } from '../components/GPUPartitions';
import { NodeTable } from '../components/NodeTable';
import { Pill } from '../components/Pill';
import { Events, Reservations } from '../components/ReservationsAndEvents';
import { SummaryCards } from '../components/SummaryCards';
import { Workloads } from '../components/Workloads';
import { formatTime } from '../lib/format';
import { summarize } from '../lib/summary';
import styles from './Dashboard.module.css';

/** 화면 한 장. 보기 전용이고 30초마다 새로 불러온다. */
export function Dashboard({ sample = false }: { sample?: boolean }) {
  const queryClient = useQueryClient();
  const nodes = useNodes();
  const gpus = useGPUs();
  const capacity = useCapacity();
  const workloads = useWorkloads();
  const reservations = useReservations();
  const events = useEvents();
  const all = [nodes, gpus, capacity, workloads, reservations, events];

  const failed = all.find((q) => q.isError);
  const updatedAt = Math.max(...all.map((q) => q.dataUpdatedAt));
  const ready = all.every((q) => q.data !== undefined);

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div className={styles.titleBox}>
          <h1 className={styles.title}>GPU Pool Manager</h1>
          <p className={styles.subtitle}>
            노드 · GPU 카드 · MIG 인스턴스 · 워크로드 현황
            {sample && <Pill tone="yellow">샘플 데이터</Pill>}
          </p>
        </div>
        <div className={styles.actions}>
          <span className={styles.updated}>
            {updatedAt > 0 ? `마지막 갱신 ${formatTime(updatedAt)}` : '불러오는 중'} ·{' '}
            {REFRESH_INTERVAL_MS / 1000}초마다 자동 갱신
          </span>
          <button
            type="button"
            className={styles.refresh}
            onClick={() => void queryClient.invalidateQueries()}
          >
            새로고침
          </button>
        </div>
      </header>

      {failed && (
        <div role="alert" className={styles.error}>
          서버2에서 데이터를 받지 못했습니다: {failed.error?.message}. 이전에 받은 값을 보여줍니다.
        </div>
      )}

      {!ready && !failed && <p className={styles.loading}>불러오는 중…</p>}

      {ready && (
        <>
          <SummaryCards
            s={summarize(
              nodes.data!.items,
              gpus.data!.items,
              workloads.data!.items,
              reservations.data!.items,
            )}
          />
          <NodeTable
            nodes={nodes.data!.items}
            gpus={gpus.data!.items}
            capacity={capacity.data!.items}
            workloads={workloads.data!.items}
          />
          <GPUPartitions
            nodes={nodes.data!.items}
            gpus={gpus.data!.items}
            capacity={capacity.data!.items}
          />
          <Workloads workloads={workloads.data!.items} />
          <Reservations reservations={reservations.data!.items} />
          <Events events={events.data!.items} gpus={gpus.data!.items} />
        </>
      )}
    </div>
  );
}
