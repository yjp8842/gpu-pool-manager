// Package fake는 GPU 없이 개발·테스트할 때 쓰는 metrics.MetricSource 구현이다 (개발 단계 2).
//
// Source는 정해 둔 스냅샷을 그대로 돌려준다. 테스트는 Set과 SetError로 회차마다 값을 바꿔
// 추가·변경·사라짐·재분할·수집 실패를 만든다. 로컬 실행(METRIC_SOURCE=fake)은 Demo를 쓴다.
package fake

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yjp8842/gpu-pool-manager/internal/metrics"
)

// Source는 Set으로 넣어 둔 스냅샷을 돌려주는 MetricSource다. 여러 고루틴에서 써도 된다.
type Source struct {
	mu    sync.Mutex
	snaps []metrics.Snapshot
	err   error
	now   func() time.Time
}

var _ metrics.MetricSource = (*Source)(nil)

// New는 snaps를 돌려주는 Source를 만든다.
func New(snaps ...metrics.Snapshot) *Source {
	return &Source{snaps: cloneAll(snaps), now: time.Now}
}

// Set은 다음 회차부터 돌려줄 스냅샷을 바꾼다. 노드를 빼면 그 노드는 읽지 못한 것과 같다.
func (s *Source) Set(snaps ...metrics.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snaps = cloneAll(snaps)
}

// SetError는 다음 회차부터 스냅샷과 함께 err을 돌려주게 한다. nil이면 오류를 없앤다.
// 일부 노드만 읽은 경우는 Set으로 그 노드만 남기고 SetError를 함께 쓴다.
func (s *Source) SetError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// Fetch는 넣어 둔 스냅샷의 복사본을 돌려준다. At이 비어 있으면 지금 시각을 넣는다.
func (s *Source) Fetch(ctx context.Context) ([]metrics.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := cloneAll(s.snaps)
	at := s.now()
	for i := range out {
		if out[i].At.IsZero() {
			out[i].At = at
		}
	}
	return out, s.err
}

// ---- 스냅샷을 만드는 도우미 ----

// GPU는 노드의 index번 GPU를 만든다. UUID는 노드 이름과 번호로 정해진다.
func GPU(node string, index int, model string, memoryMB int, migEnabled bool) metrics.GPU {
	return metrics.GPU{
		UUID:        fmt.Sprintf("GPU-fake-%s-%d", node, index),
		Index:       index,
		Model:       model,
		MemoryMB:    memoryMB,
		MIGEnabled:  &migEnabled,
		TotalSlices: metrics.TotalSlices(model),
	}
}

// Slices는 gpu를 profile 조각으로 나눈 결과를 GI ID마다 하나씩 만든다.
func Slices(gpu metrics.GPU, profile string, giIDs ...int) []metrics.Instance {
	out := make([]metrics.Instance, 0, len(giIDs))
	for _, gi := range giIDs {
		out = append(out, metrics.Instance{
			ParentUUID: gpu.UUID,
			GIID:       gi,
			Profile:    profile,
			MIGUUID:    fmt.Sprintf("MIG-%s-%d-%s", strings.TrimPrefix(gpu.UUID, "GPU-"), gi, profile),
			MemoryMB:   profileMemoryMB(profile),
		})
	}
	return out
}

// Full은 나누지 않은 GPU 전체를 조각 하나로 만든다 (프로파일 full, GI ID 0).
func Full(gpu metrics.GPU) metrics.Instance {
	return metrics.Instance{
		ParentUUID: gpu.UUID,
		Profile:    metrics.ProfileFull,
		MemoryMB:   gpu.MemoryMB,
	}
}

// WithPod는 조각을 namespace/name Pod가 쓰는 것으로 표시한 복사본을 돌려준다.
func WithPod(in metrics.Instance, namespace, name string) metrics.Instance {
	in.Pod = &metrics.PodRef{Namespace: namespace, Name: name, Container: "main"}
	return in
}

// profileMemoryMB는 "1g.5gb" 같은 프로파일 이름에서 메모리(MiB)를 읽는다. 읽지 못하면 0이다.
func profileMemoryMB(profile string) int {
	_, mem, ok := strings.Cut(profile, ".")
	if !ok {
		return 0
	}
	gb, err := strconv.Atoi(strings.TrimSuffix(mem, "gb"))
	if err != nil {
		return 0
	}
	return gb * 1024
}

// Demo는 로컬 실행용 스냅샷이다. 설계도의 데모 구성을 흉내 낸다: GKE MIG 노드 풀의 노드 한 대
// (a2-highgpu-1g, A100 40GB 1장)를 1g.5gb × 7로 나누고, 조각 두 개를 Pod가 쓴다.
func Demo() []metrics.Snapshot {
	const node, a100 = "fake-a100-mig", "NVIDIA A100-SXM4-40GB"

	g := GPU(node, 0, a100, 40960, true)
	s := metrics.Snapshot{
		Node:      node,
		GPUs:      []metrics.GPU{g},
		Instances: Slices(g, "1g.5gb", 7, 8, 9, 10, 11, 12, 13),
		LastXID:   map[string]int{g.UUID: 0},
	}
	s.Instances[0] = WithPod(s.Instances[0], "default", "train-0")
	s.Instances[1] = WithPod(s.Instances[1], "default", "train-1")

	return []metrics.Snapshot{s}
}

// cloneAll은 호출한 쪽이 결과를 고쳐도 Source 안의 값이 바뀌지 않도록 깊게 복사한다.
func cloneAll(snaps []metrics.Snapshot) []metrics.Snapshot {
	out := make([]metrics.Snapshot, len(snaps))
	for i, s := range snaps {
		s.GPUs = slices.Clone(s.GPUs)
		for j := range s.GPUs {
			if p := s.GPUs[j].MIGEnabled; p != nil {
				s.GPUs[j].MIGEnabled = new(*p)
			}
		}
		s.Instances = slices.Clone(s.Instances)
		for j := range s.Instances {
			if p := s.Instances[j].Pod; p != nil {
				s.Instances[j].Pod = new(*p)
			}
		}
		s.LastXID = maps.Clone(s.LastXID)
		out[i] = s
	}
	return out
}
