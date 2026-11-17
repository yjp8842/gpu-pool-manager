package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yjp8842/gpu-pool-manager/internal/metrics"
)

func TestFetchReturnsCopies(t *testing.T) {
	src := New(Demo()...)
	first, err := src.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// 받은 쪽이 값을 고쳐도 다음 회차에는 원래 값이 나온다.
	first[0].GPUs[0].Model = "changed"
	*first[0].GPUs[0].MIGEnabled = false
	first[0].Instances[0].Pod.Name = "changed"
	first[0].LastXID[first[0].GPUs[0].UUID] = 79

	second, err := src.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	g := second[0].GPUs[0]
	if g.Model != "NVIDIA A100-SXM4-40GB" || !*g.MIGEnabled || second[0].Instances[0].Pod.Name != "train-0" ||
		second[0].LastXID[g.UUID] != 0 {
		t.Fatalf("Fetch result shares memory with the source: %+v", second[0])
	}
}

func TestFetchSetsTimeAndError(t *testing.T) {
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	src := New(metrics.Snapshot{Node: "a"}, metrics.Snapshot{Node: "b", At: at.Add(-time.Hour)})
	src.now = func() time.Time { return at }

	snaps, err := src.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !snaps[0].At.Equal(at) || !snaps[1].At.Equal(at.Add(-time.Hour)) {
		t.Fatalf("At = %v, %v; want empty At filled with now and set At kept", snaps[0].At, snaps[1].At)
	}

	// 일부 노드만 읽은 경우: 남은 스냅샷과 오류를 함께 돌려준다.
	boom := errors.New("node b unreachable")
	src.Set(metrics.Snapshot{Node: "a"})
	src.SetError(boom)
	snaps, err = src.Fetch(t.Context())
	if !errors.Is(err, boom) || len(snaps) != 1 {
		t.Fatalf("Fetch = %d snapshots, %v; want 1 and %v", len(snaps), err, boom)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := src.Fetch(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Fetch with canceled ctx = %v", err)
	}
}

func TestDemo(t *testing.T) {
	snaps := Demo()
	if len(snaps) != 1 {
		t.Fatalf("Demo has %d nodes, want 1", len(snaps))
	}
	s := snaps[0]
	if s.Node != "fake-a100-mig" || len(s.GPUs) != 1 || s.GPUs[0].TotalSlices != 7 || !*s.GPUs[0].MIGEnabled {
		t.Fatalf("Demo node = %+v", s)
	}
	if len(s.Instances) != 7 {
		t.Fatalf("Demo has %d instances, want 7", len(s.Instances))
	}
	seen := map[int]bool{}
	for _, in := range s.Instances {
		if in.ParentUUID != s.GPUs[0].UUID || in.Profile != "1g.5gb" || in.MemoryMB != 5*1024 || seen[in.GIID] {
			t.Errorf("instance %+v", in)
		}
		seen[in.GIID] = true
	}
	if s.Instances[0].GIID != 7 || s.Instances[0].Pod.Name != "train-0" || s.Instances[1].Pod.Name != "train-1" ||
		s.Instances[2].Pod != nil {
		t.Fatalf("Demo pods = %+v, %+v, %+v", s.Instances[0], s.Instances[1], s.Instances[2])
	}
}

func TestProfileMemoryMB(t *testing.T) {
	for profile, want := range map[string]int{"1g.5gb": 5120, "3g.20gb": 20480, "full": 0, "1g.xgb": 0} {
		if got := profileMemoryMB(profile); got != want {
			t.Errorf("profileMemoryMB(%q) = %d, want %d", profile, got, want)
		}
	}
}
