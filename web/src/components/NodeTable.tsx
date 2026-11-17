import type { Capacity, GPU, Node, Workload } from '../api/client';
import { formatTime } from '../lib/format';
import { availableLabel, cardsLabel, migLabel } from '../lib/summary';
import ui from '../styles/ui.module.css';
import { ReadyPill } from './Pill';

export function NodeTable({
  nodes,
  gpus,
  capacity,
  workloads,
}: {
  nodes: Node[];
  gpus: GPU[];
  capacity: Capacity[];
  workloads: Workload[];
}) {
  const pods = (node: string) => workloads.filter((w) => w.node === node).length;

  return (
    <section aria-label="노드 상태" className={ui.card}>
      <h2 className={ui.h2}>노드 상태</h2>
      <div className={ui.tblbox}>
        <table className={ui.tbl}>
          <thead>
            <tr>
              <th>노드</th>
              <th>인스턴스 타입</th>
              <th>상태</th>
              <th>GPU 카드</th>
              <th>MIG</th>
              <th>남은 수량</th>
              <th>Pod</th>
              <th>마지막 보고</th>
            </tr>
          </thead>
          <tbody>
            {nodes.length === 0 && (
              <tr>
                <td colSpan={8} className={ui.empty}>
                  장부에 GPU 노드가 없습니다
                </td>
              </tr>
            )}
            {nodes.map((n) => {
              const mig = migLabel(n, gpus);
              return (
                <tr key={n.name}>
                  <td className={ui.strong}>{n.name}</td>
                  <td className={ui.mono}>{n.instanceType ?? '—'}</td>
                  <td>
                    <ReadyPill ready={n.ready} />
                  </td>
                  <td>{cardsLabel(n, gpus)}</td>
                  <td className={mig ? undefined : ui.muted}>{mig ?? '해당 없음'}</td>
                  <td>{availableLabel(n, capacity)}</td>
                  <td>{pods(n.name)}</td>
                  <td className={ui.muted}>{formatTime(n.lastSeenAt)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
