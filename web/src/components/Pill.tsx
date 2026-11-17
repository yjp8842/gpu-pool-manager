import type { ReactNode } from 'react';
import type { Tone } from '../lib/format';
import ui from '../styles/ui.module.css';

export function Pill({ tone, children }: { tone: Tone; children: ReactNode }) {
  return <span className={`${ui.pill} ${ui[tone]}`}>{children}</span>;
}

export function ReadyPill({ ready }: { ready: boolean | null }) {
  if (ready === null) {
    return <Pill tone="grey">K8s 정보 없음</Pill>;
  }
  return ready ? <Pill tone="green">Ready</Pill> : <Pill tone="red">NotReady</Pill>;
}
