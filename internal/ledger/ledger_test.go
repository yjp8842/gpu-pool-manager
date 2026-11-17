package ledger

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/yjp8842/gpu-pool-manager/internal/testpg"
)

// t0은 테스트의 기준 시각이다.
var t0 = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

// DB 테스트는 testpg가 띄운 PostgreSQL에서 테스트마다 새 DB를 받아 쓴다. -short이면 건너뛴다.
func TestMain(m *testing.M) {
	os.Exit(testpg.Main(m, Migrate))
}

// newTestStore는 마이그레이션을 적용한 새 DB에 연결한 Store를 돌려준다.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.Context(), testpg.NewDatabase(t))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// ---- 데이터 준비 ----

func seedNode(t *testing.T, s *Store, name string) {
	t.Helper()
	if err := s.EnsureNode(t.Context(), EnsureNodeParams{Name: name, LastSeenAt: t0}); err != nil {
		t.Fatalf("EnsureNode(%s): %v", name, err)
	}
}

func seedDevice(t *testing.T, s *Store, node, uuid string, index int32) GPUDevice {
	t.Helper()
	d, err := s.UpsertGPUDevice(t.Context(), UpsertGPUDeviceParams{
		UUID:        uuid,
		NodeName:    node,
		GPUIndex:    index,
		Model:       "NVIDIA B200",
		MemoryMB:    new(int32(183359)),
		MIGEnabled:  new(true),
		TotalSlices: new(int32(7)),
		LastSeenAt:  t0,
	})
	if err != nil {
		t.Fatalf("UpsertGPUDevice(%s): %v", uuid, err)
	}
	return d
}

func seedInstance(t *testing.T, s *Store, parent string, gi int32, profile, health string) GPUInstance {
	t.Helper()
	in, err := s.UpsertGPUInstance(t.Context(), UpsertGPUInstanceParams{
		ParentUUID: parent,
		GIID:       gi,
		Profile:    profile,
		MIGUUID:    new(fmt.Sprintf("MIG-%s-%d", parent, gi)),
		Health:     health,
		LastSeenAt: t0,
	})
	if err != nil {
		t.Fatalf("UpsertGPUInstance(%s/%d/%s): %v", parent, gi, profile, err)
	}
	return in
}

func observe(t *testing.T, s *Store, node, profile string, instanceID int64, pod string) Allocation {
	t.Helper()
	a, err := s.CreateObservedAllocation(t.Context(), CreateObservedAllocationParams{
		NodeName:        node,
		Profile:         profile,
		TargetKind:      KindPod,
		TargetNamespace: "default",
		TargetName:      pod,
		InstanceID:      new(instanceID),
		AllocatedAt:     t0,
	})
	if err != nil {
		t.Fatalf("CreateObservedAllocation(%s): %v", pod, err)
	}
	return a
}

func reserve(t *testing.T, s *Store, requestID, node, profile, pod string) Allocation {
	t.Helper()
	a, err := s.CreateReservation(t.Context(), CreateReservationParams{
		RequestID:       requestID,
		NodeName:        node,
		Profile:         profile,
		TargetKind:      KindPod,
		TargetNamespace: "default",
		TargetName:      pod,
		Requester:       "tester",
		ReservedAt:      t0,
		ExpiresAt:       t0.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateReservation(%s): %v", requestID, err)
	}
	return a
}

// ---- 확인 도우미 ----

func wantNotFound(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func wantViolation(t *testing.T, err error, is func(error, string) bool, constraint string) {
	t.Helper()
	if !is(err, constraint) {
		t.Fatalf("err = %v, want violation of %q", err, constraint)
	}
}

func wantCapacity(t *testing.T, s *Store, node, profile string, free, reserved int64) {
	t.Helper()
	got, err := s.GetCapacity(t.Context(), GetCapacityParams{NodeName: node, Profile: profile})
	if err != nil {
		t.Fatalf("GetCapacity(%s, %s): %v", node, profile, err)
	}
	if got.FreeCount != free || got.ReservedCount != reserved {
		t.Fatalf("GetCapacity(%s, %s) = free %d reserved %d, want free %d reserved %d",
			node, profile, got.FreeCount, got.ReservedCount, free, reserved)
	}
}
