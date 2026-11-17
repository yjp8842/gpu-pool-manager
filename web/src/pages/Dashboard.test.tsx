import { screen, within } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';
import { server } from '../mocks/server';
import { renderWithQuery } from '../test/render';
import { Dashboard } from './Dashboard';

async function renderDashboard() {
  renderWithQuery(<Dashboard sample />);
  // 모든 조회가 끝나면 노드 표가 나온다.
  return within(await screen.findByRole('region', { name: '노드 상태' }));
}

describe('Dashboard (목업 API)', () => {
  it('목업과 같은 구역을 위에서 아래로 그린다', async () => {
    await renderDashboard();
    expect(screen.getByRole('heading', { level: 1, name: 'GPU Pool Manager' })).toBeInTheDocument();
    expect(screen.getByText('샘플 데이터')).toBeInTheDocument();
    for (const name of [
      '요약',
      '노드 상태',
      'GPU 카드별 MIG 분할',
      '워크로드',
      '예약',
      '할당 이력',
    ]) {
      expect(screen.getByRole('region', { name })).toBeInTheDocument();
    }
  });

  it('요약 숫자는 노드·카드·조각·워크로드에서 계산한다', async () => {
    await renderDashboard();
    const summary = within(screen.getByRole('region', { name: '요약' }));
    expect(summary.getByText('Ready 1/1')).toBeInTheDocument();
    expect(summary.getByText('A100-SXM4-40GB 1')).toBeInTheDocument();
    // 조각 7개: A100 1장의 1g.5gb × 7
    expect(summary.getByText('가용 3 · 사용 중 3 · 고장 1 · 진행 중 예약 1건')).toBeInTheDocument();
    // Pod 4개 중 notebook-c만 Pending
    expect(summary.getByText('실행 중 3')).toBeInTheDocument();
  });

  it('노드 표에 카드, MIG, 남은 수량, Pod 수를 보여준다', async () => {
    const table = await renderDashboard();
    const row = table.getByText('gpu-mig-1').closest('tr')!;
    expect(within(row).getByText('A100-SXM4-40GB × 1')).toBeInTheDocument();
    expect(within(row).getByText('1 / 1 켜짐')).toBeInTheDocument();
    // 1g.5gb: 가용 3 − 예약 1 = 2
    expect(within(row).getByText('1g.5gb 2')).toBeInTheDocument();
    // gpu-mig-1에서 도는 Pod: infer-a·b·c
    expect(within(row).getByText('3')).toBeInTheDocument();
  });

  it('카드별 MIG 분할은 조각마다 상태와 사용 주체를 보여준다', async () => {
    await renderDashboard();
    const section = within(screen.getByRole('region', { name: 'GPU 카드별 MIG 분할' }));
    expect(section.getByTitle('GI 9 · 1g.5gb · Pod grim/infer-c (예약 확정)')).toHaveAttribute(
      'data-state',
      'ALLOCATED',
    );
    expect(section.getByTitle('GI 7 · 1g.5gb · Pod grim/infer-a (관측 할당)')).toHaveAttribute(
      'data-state',
      'ALLOCATED',
    );
    expect(section.getByTitle('GI 13 · 1g.5gb · 고장')).toHaveAttribute('data-state', 'UNHEALTHY');
    expect(section.getAllByTitle(/^GI 1[0-2] · 1g\.5gb · 가용$/)).toHaveLength(3);
    expect(
      section.getByText('인스턴스 7 · 가용 3 · 사용 중 3 · 고장 1 · 전체 7칸'),
    ).toBeInTheDocument();
    expect(section.getByText('1g.5gb 남은 수량 2')).toBeInTheDocument();
  });

  it('Pod 표와 예약·이력을 보여준다', async () => {
    await renderDashboard();
    const work = within(screen.getByRole('region', { name: '워크로드' }));
    expect(
      within(work.getByText('infer-a').closest('tr')!).getByText('GPU 0 · GI 7 · 1g.5gb'),
    ).toBeInTheDocument();
    expect(
      within(work.getByText('notebook-c').closest('tr')!).getByText('Pending'),
    ).toBeInTheDocument();

    const rsv = within(screen.getByRole('region', { name: '예약' }));
    expect(
      within(rsv.getByText('rsv-0042').closest('tr')!).getByText('— (Pod 대기)'),
    ).toBeInTheDocument();
    expect(
      within(rsv.getByText('rsv-0041').closest('tr')!).getByText('GPU 0 · GI 9'),
    ).toBeInTheDocument();

    const history = within(screen.getByRole('region', { name: '할당 이력' }));
    expect(history.getByText('할당 · 조각 확정')).toBeInTheDocument();
    expect(history.getByText('할당 · 관측')).toBeInTheDocument();
    expect(history.getByText('gpu-mig-1 · 1g.5gb · 남은 수량 0')).toBeInTheDocument();
    expect(history.getByText('gpu-mig-1 · GPU 0 · 나누지 않음 → 1g.5gb×7')).toBeInTheDocument();
  });

  it('예약 없는 Pod가 조각을 가져가면 늦은 예약을 용량 부족으로 표시한다', async () => {
    server.use(
      http.get('/api/v1/reservations', () =>
        HttpResponse.json({
          items: [
            {
              id: '11111111-1111-4111-8111-111111111111',
              source: 'RESERVATION',
              state: 'RESERVED',
              requestId: 'rsv-late',
              requester: 'me',
              node: 'gpu-mig-1',
              profile: '1g.5gb',
              resourceName: 'nvidia.com/gpu',
              nodeSelector: { 'cloud.google.com/gke-gpu-partition-size': '1g.5gb' },
              target: { kind: 'POD', namespace: 'default', name: 'late' },
              instance: null,
              reservedAt: new Date().toISOString(),
              expiresAt: new Date().toISOString(),
              allocatedAt: null,
              endedAt: null,
              capacityShort: true,
            },
          ],
        }),
      ),
    );
    await renderDashboard();
    expect(
      within(screen.getByRole('region', { name: '예약' })).getByText('용량 부족'),
    ).toBeInTheDocument();
  });

  it('서버2가 오류를 돌려주면 알린다', async () => {
    server.use(
      http.get('/api/v1/nodes', () =>
        HttpResponse.json({ code: 'internal', message: 'internal server error' }, { status: 500 }),
      ),
    );
    renderWithQuery(<Dashboard />);
    expect(await screen.findByRole('alert')).toHaveTextContent('internal server error');
  });
});
