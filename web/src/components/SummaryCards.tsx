import type { Summary } from '../lib/summary';
import styles from './SummaryCards.module.css';
import ui from '../styles/ui.module.css';

export function SummaryCards({ s }: { s: Summary }) {
  const cards = [
    {
      label: '노드',
      value: s.nodes,
      detail: `Ready ${s.readyNodes}/${s.nodes}`,
    },
    { label: 'GPU 카드', value: s.cards, detail: s.cardsDetail },
    {
      label: 'GPU 인스턴스 (할당 단위)',
      value: s.instances,
      detail: `가용 ${s.free} · 사용 중 ${s.allocated} · 고장 ${s.unhealthy} · 진행 중 예약 ${s.openReservations}건`,
    },
    { label: 'GPU 사용 Pod', value: s.pods, detail: `실행 중 ${s.runningPods}` },
  ];
  return (
    <section aria-label="요약" className={styles.grid}>
      {cards.map((c) => (
        <div key={c.label} className={`${ui.card} ${styles.card}`}>
          <span className={styles.label}>{c.label}</span>
          <span className={styles.value}>{c.value}</span>
          <span className={ui.note}>{c.detail}</span>
        </div>
      ))}
    </section>
  );
}
