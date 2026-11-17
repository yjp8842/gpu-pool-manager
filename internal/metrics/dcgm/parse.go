package dcgm

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/yjp8842/gpu-pool-manager/internal/metrics"
)

// 읽는 메트릭. 4.8까지의 이름과 4.9부터의 이름을 함께 둔다.
const (
	metricFBUsed          = "DCGM_FI_DEV_FB_USED" // MiB
	metricFBFree          = "DCGM_FI_DEV_FB_FREE"
	metricFBReserved      = "DCGM_FI_DEV_FB_RESERVED"
	metricFBUsedBytes     = "DCGM_FI_DEV_FB_USED_BYTES" // 바이트 (4.9부터)
	metricFBFreeBytes     = "DCGM_FI_DEV_FB_FREE_BYTES"
	metricFBReservedBytes = "DCGM_FI_DEV_FB_RESERVED_BYTES"
	metricFBCapacityBytes = "DCGM_FI_DEV_FB_CAPACITY_BYTES"
	metricXIDErrors       = "DCGM_FI_DEV_XID_ERRORS"
	metricXIDError        = "DCGM_FI_DEV_XID_ERROR" // 4.9부터
	metricMIGMode         = "DCGM_FI_DEV_MIG_MODE"
)

// 라벨. 앞의 이름이 없으면 뒤의 이름을 본다.
var (
	labelUUID      = []string{"UUID", "uuid", "gpu_uuid"}
	labelGPU       = []string{"gpu"}
	labelModel     = []string{"modelName", "model_name"}
	labelHost      = []string{"Hostname", "hostname"}
	labelGIID      = []string{"GPU_I_ID"}
	labelProfile   = []string{"GPU_I_PROFILE"}
	labelPod       = []string{"pod", "pod_name"}
	labelNamespace = []string{"namespace", "pod_namespace"}
	labelContainer = []string{"container", "container_name"}
)

const bytesPerMiB = 1 << 20

// ErrNoNode는 샘플에 노드 이름 라벨이 없고 엔드포인트에도 노드 이름이 없을 때 돌려준다.
var ErrNoNode = errors.New("노드 이름을 알 수 없다: DCGM_ENDPOINTS에 노드=URL로 적거나 dcgm-exporter가 Hostname 라벨을 내게 한다")

// Parse는 dcgm-exporter의 텍스트 출력을 노드별 스냅샷으로 바꾼다. node가 비어 있지 않으면 모든 샘플을
// 그 노드의 것으로 보고, 비어 있으면 샘플의 Hostname 라벨을 쓴다. GPU UUID가 없는 샘플(NVSwitch 등)은 건너뛴다.
// 스냅샷은 노드 이름 순서이고, 카드는 GPU 번호, 조각은 GI ID 순서다.
func Parse(r io.Reader, node string, at time.Time) ([]metrics.Snapshot, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(r)
	if err != nil {
		return nil, fmt.Errorf("parse metrics: %w", err)
	}

	gpus := map[string]*gpuAcc{} // GPU UUID → 모은 값
	for _, name := range slices.Sorted(maps.Keys(families)) {
		for _, m := range families[name].GetMetric() {
			labels := labelMap(m)
			uuid := first(labels, labelUUID)
			if uuid == "" {
				continue
			}
			g, err := gpuFor(gpus, labels, uuid, node)
			if err != nil {
				return nil, err
			}
			g.add(name, labels, m)
		}
	}

	byNode := map[string][]*gpuAcc{}
	for _, g := range gpus {
		byNode[g.node] = append(byNode[g.node], g)
	}
	out := make([]metrics.Snapshot, 0, len(byNode))
	for _, n := range slices.Sorted(maps.Keys(byNode)) {
		out = append(out, snapshot(n, byNode[n], at))
	}
	return out, nil
}

// gpuAcc는 GPU 한 장에 대해 여러 메트릭 줄에서 모은 값이다.
type gpuAcc struct {
	uuid  string
	node  string
	index int
	model string

	fb      memory // GPU 줄(조각 라벨 없음)의 메모리
	xid     *int
	migMode *bool
	pod     *metrics.PodRef // GPU 줄의 Pod. 나누지 않은 GPU를 쓰는 Pod다

	instances map[int]*instanceAcc // GI ID → 조각
}

type instanceAcc struct {
	giID    int
	profile string
	fb      memory
	pod     *metrics.PodRef
}

// memory는 메모리 메트릭을 MiB로 모은다. 값이 오지 않은 항목은 nil이다.
type memory struct {
	used, free, reserved, capacity *float64
}

func gpuFor(gpus map[string]*gpuAcc, labels map[string]string, uuid, node string) (*gpuAcc, error) {
	if g, ok := gpus[uuid]; ok {
		return g, nil
	}
	if node == "" {
		node = first(labels, labelHost)
	}
	if node == "" {
		return nil, ErrNoNode
	}
	index, err := strconv.Atoi(first(labels, labelGPU))
	if err != nil {
		return nil, fmt.Errorf("GPU %s: gpu 라벨 %q를 번호로 읽지 못했다", uuid, first(labels, labelGPU))
	}
	g := &gpuAcc{uuid: uuid, node: node, index: index, model: first(labels, labelModel), instances: map[int]*instanceAcc{}}
	gpus[uuid] = g
	return g, nil
}

// add는 메트릭 줄 하나를 반영한다. 조각 라벨이 있으면 그 조각의 값이고, 없으면 GPU의 값이다.
func (g *gpuAcc) add(name string, labels map[string]string, m *dto.Metric) {
	if g.model == "" {
		g.model = first(labels, labelModel)
	}
	v, hasValue := value(m)

	// XID와 MIG 모드는 GPU 단위 값이다. 조각 줄에 나와도 GPU에 둔다.
	switch name {
	case metricXIDErrors, metricXIDError:
		if hasValue {
			x := int(v)
			if g.xid == nil || x > *g.xid {
				g.xid = &x
			}
		}
		return
	case metricMIGMode:
		if hasValue {
			on := v == 1
			g.migMode = &on
		}
		return
	}

	pod := podRef(labels)
	profile, giText := first(labels, labelProfile), first(labels, labelGIID)
	gi, err := strconv.Atoi(giText)
	if profile == "" || err != nil {
		g.fb.add(name, v, hasValue)
		if g.pod == nil {
			g.pod = pod
		}
		return
	}
	in, ok := g.instances[gi]
	if !ok {
		in = &instanceAcc{giID: gi, profile: profile}
		g.instances[gi] = in
	}
	in.fb.add(name, v, hasValue)
	if in.pod == nil {
		in.pod = pod
	}
}

func (mem *memory) add(name string, v float64, ok bool) {
	if !ok {
		return
	}
	mib := v
	switch name {
	case metricFBUsedBytes, metricFBFreeBytes, metricFBReservedBytes, metricFBCapacityBytes:
		mib = v / bytesPerMiB
	}
	switch name {
	case metricFBUsed, metricFBUsedBytes:
		mem.used = &mib
	case metricFBFree, metricFBFreeBytes:
		mem.free = &mib
	case metricFBReserved, metricFBReservedBytes:
		mem.reserved = &mib
	case metricFBCapacityBytes:
		mem.capacity = &mib
	}
}

// totalMB는 전체 메모리(MiB)다. 용량 메트릭이 있으면 그것을, 없으면 사용 + 남음 (+ 예약)을 쓴다. 모르면 0이다.
func (mem memory) totalMB() int {
	if mem.capacity != nil {
		return int(math.Round(*mem.capacity))
	}
	if mem.used == nil || mem.free == nil {
		return 0
	}
	total := *mem.used + *mem.free
	if mem.reserved != nil {
		total += *mem.reserved
	}
	return int(math.Round(total))
}

func snapshot(node string, gpus []*gpuAcc, at time.Time) metrics.Snapshot {
	slices.SortFunc(gpus, func(a, b *gpuAcc) int {
		return cmp.Or(cmp.Compare(a.index, b.index), cmp.Compare(a.uuid, b.uuid))
	})
	s := metrics.Snapshot{Node: node, LastXID: map[string]int{}, At: at}
	for _, g := range gpus {
		migEnabled := g.migMode
		if migEnabled == nil && len(g.instances) > 0 {
			migEnabled = new(true)
		}
		gpu := metrics.GPU{
			UUID:        g.uuid,
			Index:       g.index,
			Model:       g.model,
			MemoryMB:    g.fb.totalMB(),
			MIGEnabled:  migEnabled,
			TotalSlices: metrics.TotalSlices(g.model),
		}
		s.GPUs = append(s.GPUs, gpu)
		if g.xid != nil {
			s.LastXID[g.uuid] = *g.xid
		}

		switch {
		case len(g.instances) > 0:
			for _, gi := range slices.Sorted(maps.Keys(g.instances)) {
				in := g.instances[gi]
				s.Instances = append(s.Instances, metrics.Instance{
					ParentUUID: g.uuid,
					GIID:       in.giID,
					Profile:    in.profile,
					MemoryMB:   in.fb.totalMB(),
					Pod:        in.pod,
				})
			}
		case migEnabled != nil && *migEnabled:
			// MIG가 켜졌는데 나눈 조각이 없다. 할당할 단위가 없다.
		default:
			s.Instances = append(s.Instances, metrics.Instance{
				ParentUUID: g.uuid,
				Profile:    metrics.ProfileFull,
				MemoryMB:   gpu.MemoryMB,
				Pod:        g.pod,
			})
		}
	}
	return s
}

func labelMap(m *dto.Metric) map[string]string {
	out := make(map[string]string, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		out[l.GetName()] = l.GetValue()
	}
	return out
}

// first는 names 중 처음으로 값이 있는 라벨의 값을 돌려준다.
func first(labels map[string]string, names []string) string {
	for _, n := range names {
		if v := labels[n]; v != "" {
			return v
		}
	}
	return ""
}

func podRef(labels map[string]string) *metrics.PodRef {
	name, ns := first(labels, labelPod), first(labels, labelNamespace)
	if name == "" || ns == "" {
		return nil
	}
	return &metrics.PodRef{Namespace: ns, Name: name, Container: first(labels, labelContainer)}
}

func value(m *dto.Metric) (float64, bool) {
	var v float64
	switch {
	case m.Gauge != nil:
		v = m.Gauge.GetValue()
	case m.Counter != nil:
		v = m.Counter.GetValue()
	case m.Untyped != nil:
		v = m.Untyped.GetValue()
	default:
		return 0, false
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}
