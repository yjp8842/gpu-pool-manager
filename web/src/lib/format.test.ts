import { describe, expect, it } from 'vitest';
import { eventLabel, instancesLabel, phaseTone, profileSlices, workloadLabel } from './format';

describe('format', () => {
  it('profileSlices는 프로파일의 연산 슬라이스 수를 읽는다', () => {
    expect(profileSlices('1g.5gb')).toBe(1);
    expect(profileSlices('3g.90gb')).toBe(3);
    expect(profileSlices('full', 7)).toBe(7);
    expect(profileSlices('full', null)).toBe(1);
    expect(profileSlices('weird')).toBe(1);
  });

  it('instancesLabel은 나누지 않은 GPU 여러 장을 묶는다', () => {
    const full = (gpuIndex: number) => ({
      id: gpuIndex,
      gpuUuid: 'g',
      gpuIndex,
      giId: 0,
      profile: 'full',
    });
    expect(instancesLabel([full(1), full(2)])).toBe('GPU 1, GPU 2 · 전체');
    expect(instancesLabel([{ id: 9, gpuUuid: 'g', gpuIndex: 0, giId: 7, profile: '1g.5gb' }])).toBe(
      'GPU 0 · GI 7 · 1g.5gb',
    );
    expect(instancesLabel([])).toBe('—');
  });

  it('eventLabel은 할당을 조각 확정과 관측으로 나눈다', () => {
    expect(eventLabel('ALLOCATED', 'req-1')).toBe('할당 · 조각 확정');
    expect(eventLabel('ALLOCATED', null)).toBe('할당 · 관측');
    expect(eventLabel('EXPIRED', 'req-1')).toBe('예약 만료');
  });

  it('workloadLabel과 phaseTone', () => {
    expect(workloadLabel({ kind: 'POD', namespace: 'grim', name: 'infer-a' })).toBe(
      'Pod grim/infer-a',
    );
    expect(workloadLabel(null)).toBe('—');
    expect(phaseTone('Running')).toBe('green');
    expect(phaseTone('Terminating')).toBe('yellow');
    expect(phaseTone('Succeeded')).toBe('grey');
  });
});
