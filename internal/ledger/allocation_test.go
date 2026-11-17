package ledger

import (
	"testing"
	"time"
)

func TestReservationLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	seedNode(t, s, "node-a")
	seedDevice(t, s, "node-a", "GPU-a0", 0)
	in := seedInstance(t, s, "GPU-a0", 7, "1g.23gb", HealthOK)

	// 예약: 노드 + 프로파일 단위, 조각은 아직 정해지지 않는다.
	r := reserve(t, s, "req-1", "node-a", "1g.23gb", "train-0")
	if r.Source != SourceReservation || r.State != StateReserved || r.InstanceID != nil {
		t.Fatalf("reservation = %+v", r)
	}
	wantCapacity(t, s, "node-a", "1g.23gb", 1, 1)

	byReq, err := s.GetAllocationByRequestID(ctx, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if byReq.ID != r.ID {
		t.Fatalf("GetAllocationByRequestID returned %s, want %s", byReq.ID, r.ID)
	}

	// 확정: 대상 Pod가 관측된 조각에 묶는다.
	bound, err := s.BindReservation(ctx, BindReservationParams{
		ID: r.ID, InstanceID: new(in.ID), AllocatedAt: t0.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if bound.State != StateAllocated || bound.InstanceID == nil || *bound.InstanceID != in.ID {
		t.Fatalf("bound = %+v", bound)
	}
	wantCapacity(t, s, "node-a", "1g.23gb", 0, 0)

	// 이미 확정된 예약은 다시 묶이지 않는다.
	_, err = s.BindReservation(ctx, BindReservationParams{ID: r.ID, InstanceID: new(in.ID), AllocatedAt: t0})
	wantNotFound(t, err)

	// 반납: 조각이 다시 비어 있게 된다.
	closed, err := s.CloseAllocation(ctx, CloseAllocationParams{
		ID: r.ID, State: StateReleased, EndedAt: t0.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if closed.State != StateReleased || closed.EndedAt == nil || closed.InstanceID == nil {
		t.Fatalf("closed = %+v", closed)
	}
	wantCapacity(t, s, "node-a", "1g.23gb", 1, 0)

	// 닫힌 행은 다시 닫히지 않는다.
	_, err = s.CloseAllocation(ctx, CloseAllocationParams{ID: r.ID, State: StateReleased, EndedAt: t0})
	wantNotFound(t, err)
}

func TestRequestIDUnique(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	seedNode(t, s, "node-a")
	seedDevice(t, s, "node-a", "GPU-a0", 0)
	in7 := seedInstance(t, s, "GPU-a0", 7, "1g.23gb", HealthOK)
	in8 := seedInstance(t, s, "GPU-a0", 8, "1g.23gb", HealthOK)

	reserve(t, s, "req-1", "node-a", "1g.23gb", "train-0")
	_, err := s.CreateReservation(ctx, CreateReservationParams{
		RequestID: "req-1", NodeName: "node-a", Profile: "1g.23gb",
		TargetKind: KindPod, TargetNamespace: "default", TargetName: "train-1",
		Requester: "tester", ReservedAt: t0, ExpiresAt: t0.Add(5 * time.Minute),
	})
	wantViolation(t, err, IsUniqueViolation, ConstraintRequestID)

	// 관측 할당은 요청 ID가 NULL이라 여러 개여도 걸리지 않는다.
	observe(t, s, "node-a", "1g.23gb", in7.ID, "direct-0")
	observe(t, s, "node-a", "1g.23gb", in8.ID, "direct-1")
}

func TestOneAllocationPerInstance(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	seedNode(t, s, "node-a")
	seedDevice(t, s, "node-a", "GPU-a0", 0)
	in := seedInstance(t, s, "GPU-a0", 7, "1g.23gb", HealthOK)

	first := observe(t, s, "node-a", "1g.23gb", in.ID, "direct-0")

	// 관측 경로로 같은 조각을 두 번 할당할 수 없다.
	_, err := s.CreateObservedAllocation(ctx, CreateObservedAllocationParams{
		NodeName: "node-a", Profile: "1g.23gb", TargetKind: KindPod,
		TargetNamespace: "default", TargetName: "direct-1", InstanceID: new(in.ID), AllocatedAt: t0,
	})
	wantViolation(t, err, IsUniqueViolation, ConstraintInstanceAllocated)

	// 예약 경로도 같은 인덱스에 막힌다.
	r := reserve(t, s, "req-1", "node-a", "1g.23gb", "train-0")
	_, err = s.BindReservation(ctx, BindReservationParams{ID: r.ID, InstanceID: new(in.ID), AllocatedAt: t0})
	wantViolation(t, err, IsUniqueViolation, ConstraintInstanceAllocated)

	// 앞의 할당이 반납되면 묶을 수 있다.
	if _, err := s.CloseAllocation(ctx, CloseAllocationParams{ID: first.ID, State: StateReleased, EndedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindReservation(ctx, BindReservationParams{ID: r.ID, InstanceID: new(in.ID), AllocatedAt: t0}); err != nil {
		t.Fatalf("bind after release: %v", err)
	}
}

func TestAllocationStateChecks(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	seedNode(t, s, "node-a")
	seedDevice(t, s, "node-a", "GPU-a0", 0)
	in := seedInstance(t, s, "GPU-a0", 7, "1g.23gb", HealthOK)

	t.Run("reservation cannot be released before it is allocated", func(t *testing.T) {
		r := reserve(t, s, "req-release", "node-a", "1g.23gb", "train-0")
		_, err := s.CloseAllocation(ctx, CloseAllocationParams{ID: r.ID, State: StateReleased, EndedAt: t0})
		wantViolation(t, err, IsCheckViolation, "allocation_allocated_at_check")
	})
	t.Run("observed allocation cannot expire", func(t *testing.T) {
		a := observe(t, s, "node-a", "1g.23gb", in.ID, "direct-0")
		_, err := s.CloseAllocation(ctx, CloseAllocationParams{ID: a.ID, State: StateExpired, EndedAt: t0})
		wantViolation(t, err, IsCheckViolation, "allocation_observed_fields_check")
	})
	t.Run("closing needs a closed state", func(t *testing.T) {
		r := reserve(t, s, "req-open", "node-a", "1g.23gb", "train-1")
		_, err := s.CloseAllocation(ctx, CloseAllocationParams{ID: r.ID, State: StateReserved, EndedAt: t0})
		wantViolation(t, err, IsCheckViolation, "allocation_ended_at_check")
	})
	t.Run("unknown state", func(t *testing.T) {
		// 확정된 예약이어야 다른 CHECK에 먼저 걸리지 않는다.
		other := seedInstance(t, s, "GPU-a0", 8, "1g.23gb", HealthOK)
		r := reserve(t, s, "req-unknown", "node-a", "1g.23gb", "train-2")
		if _, err := s.BindReservation(ctx, BindReservationParams{ID: r.ID, InstanceID: new(other.ID), AllocatedAt: t0}); err != nil {
			t.Fatal(err)
		}
		_, err := s.CloseAllocation(ctx, CloseAllocationParams{ID: r.ID, State: "DONE", EndedAt: t0})
		wantViolation(t, err, IsCheckViolation, "allocation_state_check")
	})
	t.Run("reservation needs requester", func(t *testing.T) {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO allocation (source, state, request_id, node_name, profile,
				target_kind, target_namespace, target_name, reserved_at, expires_at)
			VALUES ('RESERVATION', 'RESERVED', 'req-x', 'node-a', '1g.23gb',
				'POD', 'default', 'train-3', $1, $2)`, t0, t0.Add(time.Minute))
		wantViolation(t, err, IsCheckViolation, "allocation_reservation_fields_check")
	})
	t.Run("observed allocation has no request ID", func(t *testing.T) {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO allocation (source, state, request_id, node_name, profile,
				target_kind, target_namespace, target_name, allocated_at)
			VALUES ('OBSERVED', 'ALLOCATED', 'req-y', 'node-a', '1g.23gb',
				'POD', 'default', 'direct-1', $1)`, t0)
		wantViolation(t, err, IsCheckViolation, "allocation_observed_fields_check")
	})
	t.Run("reserved row is not bound to an instance", func(t *testing.T) {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO allocation (source, state, request_id, node_name, profile,
				target_kind, target_namespace, target_name, requester, instance_id, reserved_at, expires_at)
			VALUES ('RESERVATION', 'RESERVED', 'req-z', 'node-a', '1g.23gb',
				'POD', 'default', 'train-4', 'tester', $1, $2, $3)`, in.ID, t0, t0.Add(time.Minute))
		wantViolation(t, err, IsCheckViolation, "allocation_reserved_unbound_check")
	})
}

func TestCapacity(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	seedNode(t, s, "node-a")
	seedNode(t, s, "node-b")
	seedDevice(t, s, "node-a", "GPU-a0", 0)
	seedDevice(t, s, "node-b", "GPU-b0", 0)

	// node-a: 1g.23gb 5개(그중 1개 고장), 2g.45gb 1개. node-b: 1g.23gb 1개.
	var small []GPUInstance
	for gi := int32(7); gi <= 10; gi++ {
		small = append(small, seedInstance(t, s, "GPU-a0", gi, "1g.23gb", HealthOK))
	}
	seedInstance(t, s, "GPU-a0", 11, "1g.23gb", HealthUnhealthy)
	seedInstance(t, s, "GPU-a0", 3, "2g.45gb", HealthOK)
	seedInstance(t, s, "GPU-b0", 7, "1g.23gb", HealthOK)

	// 빈 조각 4개 중 1개는 관측 할당, 예약 1개는 열려 있고 1개는 취소됐다.
	observed := observe(t, s, "node-a", "1g.23gb", small[0].ID, "direct-0")
	reserve(t, s, "req-open", "node-a", "1g.23gb", "train-0")
	cancelled := reserve(t, s, "req-cancel", "node-a", "1g.23gb", "train-1")
	if _, err := s.CloseAllocation(ctx, CloseAllocationParams{ID: cancelled.ID, State: StateCancelled, EndedAt: t0}); err != nil {
		t.Fatal(err)
	}

	wantCapacity(t, s, "node-a", "1g.23gb", 3, 1) // 남은 수량 2
	wantCapacity(t, s, "node-a", "2g.45gb", 1, 0)
	wantCapacity(t, s, "node-b", "1g.23gb", 1, 0)
	wantCapacity(t, s, "node-c", "1g.23gb", 0, 0)

	// 반납된 할당은 빈 조각으로 돌아온다.
	if _, err := s.CloseAllocation(ctx, CloseAllocationParams{ID: observed.ID, State: StateReleased, EndedAt: t0}); err != nil {
		t.Fatal(err)
	}
	wantCapacity(t, s, "node-a", "1g.23gb", 4, 1)
}

func TestInTxRollsBack(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	err := s.InTx(ctx, func(q *Queries) error {
		if err := q.EnsureNode(ctx, EnsureNodeParams{Name: "node-a", LastSeenAt: t0}); err != nil {
			return err
		}
		// 없는 노드를 참조해 실패시키면 앞의 노드 추가도 되돌아간다.
		_, err := q.UpsertGPUDevice(ctx, UpsertGPUDeviceParams{
			UUID: "GPU-x", NodeName: "node-missing", Model: "NVIDIA B200", LastSeenAt: t0,
		})
		return err
	})
	wantViolation(t, err, IsForeignKeyViolation, "")

	_, err = s.GetNode(ctx, "node-a")
	wantNotFound(t, err)

	if err := s.InTx(ctx, func(q *Queries) error {
		return q.EnsureNode(ctx, EnsureNodeParams{Name: "node-a", LastSeenAt: t0})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNode(ctx, "node-a"); err != nil {
		t.Fatalf("committed node not found: %v", err)
	}
}
