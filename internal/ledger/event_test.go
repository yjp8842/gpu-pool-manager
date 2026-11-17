package ledger

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestEventsAppendOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	reserved, err := s.AppendEvent(ctx, AppendEventParams{
		OccurredAt:        t0,
		Event:             EventReserved,
		NodeName:          new("node-a"),
		Profile:           new("1g.23gb"),
		WorkloadKind:      new(KindPod),
		WorkloadNamespace: new("default"),
		WorkloadName:      new("train-0"),
		Requester:         new("tester"),
		RequestID:         new("req-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 같은 할당의 이력 두 줄과 재분할 이력 한 줄. 이력은 allocation을 참조하지 않아 아무 ID나 넣을 수 있다.
	id := uuid.New()
	for i, ev := range []AppendEventParams{
		{OccurredAt: t0.Add(time.Minute), Event: EventAllocated, NodeName: new("node-a"),
			GPUUUID: new("GPU-a0"), GIID: new(int32(7)), Profile: new("1g.23gb"), MIGUUID: new("MIG-old"),
			AllocationID: new(id)},
		{OccurredAt: t0.Add(2 * time.Minute), Event: EventReleased, NodeName: new("node-a"),
			GPUUUID: new("GPU-a0"), GIID: new(int32(7)), Profile: new("1g.23gb"), MIGUUID: new("MIG-old"),
			AllocationID: new(id)},
		{OccurredAt: t0.Add(3 * time.Minute), Event: EventMIGReconfigured, NodeName: new("node-a"),
			GPUUUID: new("GPU-a0"), Message: new("1g.23gb x7 -> 2g.45gb x3")},
	} {
		if _, err := s.AppendEvent(ctx, ev); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}

	recent, err := s.ListEvents(ctx, ListEventsParams{RowLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].Event != EventMIGReconfigured || recent[1].Event != EventReleased {
		t.Fatalf("ListEvents(2) = %v, want newest first", eventNames(recent))
	}

	byAlloc, err := s.ListEventsByAllocation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(byAlloc) != 2 || byAlloc[0].Event != EventAllocated || byAlloc[1].Event != EventReleased {
		t.Fatalf("ListEventsByAllocation = %v, want oldest first", eventNames(byAlloc))
	}

	// 재분할 뒤에도 이전 조각의 고정 키로 이력을 찾는다.
	byKey, err := s.ListEventsByInstanceKey(ctx, ListEventsByInstanceKeyParams{
		GPUUUID: "GPU-a0", GIID: 7, Profile: "1g.23gb",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(byKey) != 2 {
		t.Fatalf("ListEventsByInstanceKey returned %d events, want 2", len(byKey))
	}

	// 한 번 쓴 이력은 고치거나 지울 수 없다.
	for _, stmt := range []string{
		"UPDATE allocation_event SET message = 'edited' WHERE id = $1",
		"DELETE FROM allocation_event WHERE id = $1",
	} {
		_, err := s.pool.Exec(ctx, stmt, reserved.ID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23001" {
			t.Errorf("%q: err = %v, want restrict_violation (23001)", stmt, err)
		}
	}

	_, err = s.AppendEvent(ctx, AppendEventParams{OccurredAt: t0, Event: "SOMETHING"})
	wantViolation(t, err, IsCheckViolation, "allocation_event_event_check")
}

func eventNames(events []AllocationEvent) []string {
	names := make([]string, len(events))
	for i, e := range events {
		names[i] = e.Event
	}
	return names
}
