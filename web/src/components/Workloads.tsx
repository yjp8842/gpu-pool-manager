import type { Workload } from '../api/client';
import { instancesLabel, phaseTone } from '../lib/format';
import ui from '../styles/ui.module.css';
import { Pill } from './Pill';

export function Workloads({ workloads }: { workloads: Workload[] }) {
  return (
    <section aria-label="워크로드" className={ui.card}>
      <h2 className={ui.h2}>GPU 사용 Pod</h2>
      <div className={ui.tblbox}>
        <table className={ui.tbl}>
          <thead>
            <tr>
              <th>이름</th>
              <th>네임스페이스</th>
              <th>노드</th>
              <th>상태</th>
              <th>GPU 인스턴스</th>
            </tr>
          </thead>
          <tbody>
            {workloads.length === 0 && <EmptyRow cols={5} text="GPU를 쓰는 Pod가 없습니다" />}
            {workloads.map((w) => (
              <tr key={`${w.namespace}/${w.name}`}>
                <td className={ui.strong}>{w.name}</td>
                <td>{w.namespace}</td>
                <td className={w.node ? undefined : ui.muted}>{w.node ?? '—'}</td>
                <td>
                  <Pill tone={phaseTone(w.phase)}>{w.phase}</Pill>
                </td>
                <td className={w.instances.length ? undefined : ui.muted}>
                  {instancesLabel(w.instances)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

export function EmptyRow({ cols, text }: { cols: number; text: string }) {
  return (
    <tr>
      <td colSpan={cols} className={ui.empty}>
        {text}
      </td>
    </tr>
  );
}
