import type { Capacity, GPU, Instance, Node } from '../api/client';
import {
  displayModel,
  instanceUser,
  profileLabel,
  profileSlices,
  shortModel,
  workloadLabel,
} from '../lib/format';
import ui from '../styles/ui.module.css';
import { Pill, ReadyPill } from './Pill';
import styles from './GPUPartitions.module.css';

/** 카드별 MIG 분할. 노드마다 카드를 조각 막대로 그린다. */
export function GPUPartitions({
  nodes,
  gpus,
  capacity,
}: {
  nodes: Node[];
  gpus: GPU[];
  capacity: Capacity[];
}) {
  return (
    <section aria-label="GPU 카드별 MIG 분할" className={`${ui.card} ${styles.section}`}>
      <div className={ui.head}>
        <h2 className={ui.h2}>GPU 카드별 MIG 분할</h2>
        <div className={styles.legend}>
          <span>
            <span className={`${styles.swatch} ${styles.free}`} />
            가용
          </span>
          <span>
            <span className={`${styles.swatch} ${styles.pod}`} />
            사용 중 · Pod
          </span>
          <span>
            <span className={`${styles.swatch} ${styles.bad}`} />
            고장
          </span>
        </div>
      </div>

      {nodes.map((n) => (
        <NodeGroup
          key={n.name}
          node={n}
          gpus={gpus.filter((g) => g.node === n.name)}
          capacity={capacity.filter((c) => c.node === n.name)}
        />
      ))}

      <p className={ui.note}>
        칸 너비는 연산 슬라이스 수에 비례하고, 전체 칸 수는 GPU마다 다릅니다 (A100·B200 7칸, RTX PRO
        6000 4칸). 칸에 마우스를 올리면 GI ID와 사용 주체가 표시됩니다.
      </p>
    </section>
  );
}

function NodeGroup({ node, gpus, capacity }: { node: Node; gpus: GPU[]; capacity: Capacity[] }) {
  const migGPUs = gpus.filter((g) => g.migEnabled === true);
  const fullGPUs = gpus.filter((g) => g.migEnabled !== true);

  return (
    <div className={styles.group}>
      <div className={styles.groupHead}>
        <h3 className={ui.h3}>{node.name}</h3>
        <ReadyPill ready={node.ready} />
        {node.instanceType && <Pill tone="grey">{node.instanceType}</Pill>}
      </div>

      {migGPUs.map((g) => (
        <MIGCard key={g.uuid} gpu={g} />
      ))}

      {fullGPUs.length > 0 && (
        <div className={styles.miniGrid}>
          {fullGPUs.map((g) => (
            <div key={g.uuid} className={styles.mini}>
              <div className={styles.miniHead}>
                <span className={styles.miniTitle}>
                  GPU {g.index} · {shortModel(g.model)}
                </span>
                <Pill tone="grey">{g.migEnabled === false ? 'MIG 꺼짐' : 'MIG 알 수 없음'}</Pill>
              </div>
              <div className={`${styles.bar} ${styles.barSmall}`}>
                {g.instances.map((in_) => (
                  <Slice key={in_.id} in_={in_} grow={1} />
                ))}
              </div>
            </div>
          ))}
        </div>
      )}

      {gpus.length === 0 && (
        <p className={ui.note}>이 노드의 GPU 카드 정보가 아직 없습니다 (DCGM 수집 전).</p>
      )}

      {capacity.length > 0 && (
        <div className={styles.caps}>
          {capacity.map((c) => (
            <span key={c.profile} className={styles.cap}>
              <b>
                {profileLabel(c.profile)} 남은 수량 {c.available}
              </b>
              <span>
                = 가용 {c.free} − 예약 {c.reserved}
              </span>
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

function MIGCard({ gpu }: { gpu: GPU }) {
  const total = gpu.totalSlices;
  const used = gpu.instances.reduce((n, i) => n + profileSlices(i.profile, total), 0);
  const unpartitioned = total ? Math.max(total - used, 0) : 0;
  const count = (state: Instance['state']) => gpu.instances.filter((i) => i.state === state).length;
  const profiles = [...new Set(gpu.instances.map((i) => i.profile))];
  const memory = gpu.memoryMb ? ` · ${Math.round(gpu.memoryMb / 1024)}GB` : '';

  return (
    <div className={styles.gpu}>
      <div className={styles.gpuHead}>
        <div className={styles.gpuTitle}>
          <span className={styles.gpuName}>
            GPU {gpu.index} · {displayModel(gpu.model)}
            {memory}
          </span>
          <span className={ui.mono + ' ' + ui.muted}>{gpu.uuid}</span>
        </div>
        <Pill tone="indigo">
          MIG 켜짐
          {profiles.length > 0
            ? ` · ${profiles.map((p) => `${p} × ${gpu.instances.filter((i) => i.profile === p).length}`).join(', ')}`
            : ' · 조각 없음'}
        </Pill>
      </div>
      <div className={styles.bar}>
        {gpu.instances.map((in_) => (
          <Slice key={in_.id} in_={in_} grow={profileSlices(in_.profile, total)} />
        ))}
        {unpartitioned > 0 && (
          <div
            className={`${styles.slice} ${styles.empty}`}
            style={{ flexGrow: unpartitioned }}
            title={`나누지 않은 칸 ${unpartitioned}`}
          >
            <b>빈 칸</b>
          </div>
        )}
      </div>
      <span className={ui.note}>
        인스턴스 {gpu.instances.length} · 가용 {count('FREE')} · 사용 중 {count('ALLOCATED')} · 고장{' '}
        {count('UNHEALTHY')}
        {total ? ` · 전체 ${total}칸` : ''}
      </span>
    </div>
  );
}

function Slice({ in_, grow }: { in_: Instance; grow: number }) {
  const user = instanceUser(in_);
  let tone = styles.free;
  let label = '가용';
  if (in_.state === 'UNHEALTHY') {
    tone = styles.bad;
    label = '고장';
  } else if (user) {
    tone = styles.pod;
    label = user;
  }
  const how =
    in_.allocation?.source === 'RESERVATION'
      ? ' (예약 확정)'
      : in_.allocation?.source === 'OBSERVED'
        ? ' (관측 할당)'
        : '';
  const who = in_.allocation
    ? workloadLabel(in_.allocation.target)
    : in_.observedPod
      ? `Pod ${in_.observedPod.namespace}/${in_.observedPod.name}`
      : label;
  const stale = in_.missedCount > 0 ? ` · 최근 ${in_.missedCount}회 수집에서 안 보임` : '';
  const title =
    in_.profile === 'full'
      ? `GPU 전체 · ${who}${how}${stale}`
      : `GI ${in_.giId} · ${in_.profile} · ${who}${how}${stale}`;

  return (
    <div
      className={`${styles.slice} ${tone} ${in_.missedCount > 0 ? styles.stale : ''}`}
      style={{ flexGrow: grow }}
      title={title}
      data-state={in_.state}
    >
      <b>{profileLabel(in_.profile)}</b>
      <span>{label}</span>
    </div>
  );
}
