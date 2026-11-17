package allocation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yjp8842/gpu-pool-manager/internal/history"
	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
)

// reconcileLockKey는 정합 작업 잠금 키다. 서버2가 여러 개 떠도 정합 작업은 한 곳에서만 돈다.
const reconcileLockKey int64 = 0x6770755f72656331 // "gpu_rec1"

// ReconcileStats는 정합 작업 한 번의 결과다.
type ReconcileStats struct {
	Expired  int // 만료 시각이 지나 닫은 예약
	Released int // 대상 Pod가 조각에서 사라져 반납한 할당
	Bound    int // 대상 Pod가 관측돼 조각에 묶은 예약
	Observed int // 예약 없이 관측돼 새로 만든 관측 할당
	Skipped  bool
}

func (s ReconcileStats) changed() bool {
	return s.Expired+s.Released+s.Bound+s.Observed > 0
}

// Run은 interval마다 Reconcile을 돌린다. ctx가 끝나면 돌아온다.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		st, err := s.Reconcile(ctx, s.now())
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			s.logger.Warn("reconcile failed", "err", err)
		case st.changed():
			s.logger.Info("reconcile", "expired", st.Expired, "released", st.Released, "bound", st.Bound, "observed", st.Observed)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Reconcile은 서버1이 쓴 관측 값을 보고 할당 상태를 바꾼다. 한 트랜잭션으로 아래 순서대로 처리한다.
//  1. 만료: 확정 없이 만료 시각이 지난 예약을 EXPIRED로 닫는다.
//  2. 반납: 묶인 조각이 사라졌거나, 그 조각에서 대상 Pod가 더 이상 관측되지 않는 할당을 RELEASED로 닫는다.
//  3. 확정: 대상 Pod가 예약한 노드의 같은 프로파일 조각에서 관측되면 예약을 그 조각에 묶는다.
//  4. 관측 할당: 예약 없이 Pod가 쓰고 있는 조각에 OBSERVED 할당을 만든다.
//
// 반납을 확정보다 먼저 해서, 조각의 Pod가 바뀐 경우 같은 회차에 새 Pod로 다시 묶을 수 있게 한다.
func (s *Service) Reconcile(ctx context.Context, now time.Time) (ReconcileStats, error) {
	var st ReconcileStats
	err := s.store.InTx(ctx, func(q *ledger.Queries) error {
		st = ReconcileStats{}
		locked, err := q.TryLockReconcile(ctx, reconcileLockKey)
		if err != nil {
			return fmt.Errorf("lock: %w", err)
		}
		if !locked {
			st.Skipped = true
			return nil
		}
		if st.Expired, err = s.expire(ctx, q, now); err != nil {
			return err
		}
		if st.Released, err = s.release(ctx, q, now); err != nil {
			return err
		}
		if st.Bound, err = s.bind(ctx, q, now); err != nil {
			return err
		}
		st.Observed, err = s.observe(ctx, q, now)
		return err
	})
	if err != nil {
		return ReconcileStats{}, err
	}
	return st, nil
}

func (s *Service) expire(ctx context.Context, q *ledger.Queries, now time.Time) (int, error) {
	expired, err := q.ExpireReservations(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("expire: %w", err)
	}
	for _, a := range expired {
		if err := history.Allocation(ctx, q, now, ledger.EventExpired, a, nil, ""); err != nil {
			return 0, err
		}
	}
	return len(expired), nil
}

func (s *Service) release(ctx context.Context, q *ledger.Queries, now time.Time) (int, error) {
	rows, err := q.ListAllocatedForReconcile(ctx)
	if err != nil {
		return 0, fmt.Errorf("list allocated: %w", err)
	}
	n := 0
	for _, r := range rows {
		a := r.Allocation
		var reason string
		var in *history.Instance
		switch {
		case a.InstanceID == nil || r.InstanceGPUUUID == nil:
			reason = "조각이 사라짐 (재분할 또는 노드 삭제)"
		default:
			in = &history.Instance{GPUUUID: *r.InstanceGPUUUID, GIID: *r.InstanceGIID, Profile: *r.InstanceProfile, MIGUUID: r.InstanceMIGUUID}
			if deref(r.InstancePodNamespace) == a.TargetNamespace && deref(r.InstancePodName) == a.TargetName {
				continue // 대상 Pod가 아직 쓰고 있다
			}
			reason = "대상 Pod가 조각에서 사라짐"
		}
		closed, err := q.CloseAllocation(ctx, ledger.CloseAllocationParams{ID: a.ID, State: ledger.StateReleased, EndedAt: now})
		if errors.Is(err, ledger.ErrNotFound) {
			continue // 반납 호출이 먼저 닫았다
		}
		if err != nil {
			return 0, fmt.Errorf("release %s: %w", a.ID, err)
		}
		if err := history.Allocation(ctx, q, now, ledger.EventReleased, closed, in, reason); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

func (s *Service) bind(ctx context.Context, q *ledger.Queries, now time.Time) (int, error) {
	reserved, err := q.ListReservedAllocations(ctx)
	if err != nil {
		return 0, fmt.Errorf("list reserved: %w", err)
	}
	n := 0
	for _, a := range reserved {
		in, err := q.FindBindableInstance(ctx, ledger.FindBindableInstanceParams{
			NodeName: a.NodeName, Profile: a.Profile, PodNamespace: a.TargetNamespace, PodName: a.TargetName,
		})
		if errors.Is(err, ledger.ErrNotFound) {
			continue // 대상 Pod가 아직 관측되지 않았다
		}
		if err != nil {
			return 0, fmt.Errorf("find instance for %s: %w", a.ID, err)
		}
		bound, err := q.BindReservation(ctx, ledger.BindReservationParams{ID: a.ID, InstanceID: &in.ID, AllocatedAt: now})
		if errors.Is(err, ledger.ErrNotFound) {
			continue // 취소 호출이 먼저 닫았다
		}
		if err != nil {
			return 0, fmt.Errorf("bind %s: %w", a.ID, err)
		}
		hi := &history.Instance{GPUUUID: in.ParentUUID, GIID: in.GIID, Profile: in.Profile, MIGUUID: in.MIGUUID}
		if err := history.Allocation(ctx, q, now, ledger.EventAllocated, bound, hi, "예약 확정"); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

func (s *Service) observe(ctx context.Context, q *ledger.Queries, now time.Time) (int, error) {
	rows, err := q.ListObservedUnallocatedInstances(ctx)
	if err != nil {
		return 0, fmt.Errorf("list observed: %w", err)
	}
	for _, r := range rows {
		in := r.GPUInstance
		a, err := q.CreateObservedAllocation(ctx, ledger.CreateObservedAllocationParams{
			NodeName:        r.NodeName,
			Profile:         in.Profile,
			TargetKind:      ledger.KindPod,
			TargetNamespace: deref(in.ObservedPodNamespace),
			TargetName:      deref(in.ObservedPodName),
			InstanceID:      &in.ID,
			AllocatedAt:     now,
		})
		if err != nil {
			return 0, fmt.Errorf("observed allocation for instance %d: %w", in.ID, err)
		}
		hi := &history.Instance{GPUUUID: in.ParentUUID, GIID: in.GIID, Profile: in.Profile, MIGUUID: in.MIGUUID}
		if err := history.Allocation(ctx, q, now, ledger.EventAllocated, a, hi, "관측 할당 (예약 없음)"); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
