package collector

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
	"github.com/yjp8842/gpu-pool-manager/internal/metrics"
	"github.com/yjp8842/gpu-pool-manager/internal/metrics/fake"
	"github.com/yjp8842/gpu-pool-manager/internal/testpg"
)

func TestMain(m *testing.M) {
	os.Exit(testpg.Main(m, ledger.Migrate))
}

const (
	b200 = "NVIDIA B200"
	a100 = "NVIDIA A100-SXM4-40GB" // fake.Demo의 GPU
)

var t0 = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

type env struct {
	t     *testing.T
	store *ledger.Store
	src   *fake.Source
	c     *Collector
	round int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store, err := ledger.Open(t.Context(), testpg.NewDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	src := fake.New()
	return &env{t: t, store: store, src: src, c: New(src, store, slog.New(slog.NewTextHandler(io.Discard, nil)))}
}

// collect는 snaps를 한 회차로 반영한다. 회차마다 시각이 1분씩 늘어난다.
func (e *env) collect(snaps ...metrics.Snapshot) Stats {
	e.t.Helper()
	e.round++
	at := t0.Add(time.Duration(e.round) * time.Minute)
	for i := range snaps {
		snaps[i].At = at
	}
	e.src.Set(snaps...)
	st, err := e.c.RunOnce(e.t.Context())
	if err != nil {
		e.t.Fatalf("round %d: %v", e.round, err)
	}
	return st
}

func (e *env) device(uuid string) (ledger.GPUDevice, bool) {
	e.t.Helper()
	d, err := e.store.GetGPUDevice(e.t.Context(), uuid)
	if errors.Is(err, ledger.ErrNotFound) {
		return d, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return d, true
}

func (e *env) instances(node string) []ledger.GPUInstance {
	e.t.Helper()
	rows, err := e.store.ListGPUInstances(e.t.Context(), ledger.ListGPUInstancesParams{NodeName: &node})
	if err != nil {
		e.t.Fatal(err)
	}
	out := make([]ledger.GPUInstance, len(rows))
	for i, r := range rows {
		out[i] = r.GPUInstance
	}
	return out
}

func (e *env) events() []ledger.AllocationEvent {
	e.t.Helper()
	evs, err := e.store.ListEvents(e.t.Context(), ledger.ListEventsParams{RowLimit: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

// node는 B200 카드 n장짜리 노드 스냅샷의 뼈대를 만든다. 조각은 호출한 쪽이 붙인다.
func node(name string, n int, migGPUs ...int) (metrics.Snapshot, []metrics.GPU) {
	s := metrics.Snapshot{Node: name, LastXID: map[string]int{}}
	for i := range n {
		g := fake.GPU(name, i, b200, 183359, contains(migGPUs, i))
		s.GPUs = append(s.GPUs, g)
		s.LastXID[g.UUID] = 0
	}
	return s, s.GPUs
}

// twoNodes는 노드 두 대짜리 시험 데이터다. node-a는 MIG GPU 1장(1g.23gb × 7), node-b는 나누지 않은 GPU 2장이다.
// 노드가 여럿일 때의 규칙(일부 노드만 읽힘, K8s가 모르는 노드)을 확인할 때 쓴다. fake.Demo는 노드가 한 대다.
func twoNodes() []metrics.Snapshot {
	a, ga := node("node-a", 1, 0)
	a.Instances = fake.Slices(ga[0], "1g.23gb", 7, 8, 9, 10, 11, 12, 13)
	b, gb := node("node-b", 2)
	b.Instances = []metrics.Instance{fake.Full(gb[0]), fake.Full(gb[1])}
	return []metrics.Snapshot{a, b}
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ---- 추가 ----

func TestFirstRoundAddsEverything(t *testing.T) {
	e := newEnv(t)
	st := e.collect(fake.Demo()...)
	if st.Nodes != 1 || st.GPUs != 1 || st.Instances != 7 || st.changed() {
		t.Fatalf("stats = %+v, want 1 node, 1 GPU, 7 instances, nothing missed", st)
	}

	// DCGM에서 먼저 보인 노드는 이름만 들어간다 (K8s 값은 kube 패키지의 watch가 채운다).
	n, err := e.store.GetNode(t.Context(), "fake-a100-mig")
	if err != nil {
		t.Fatal(err)
	}
	if n.Ready != nil || n.InstanceType != nil {
		t.Fatalf("node = %+v", n)
	}

	d, ok := e.device("GPU-fake-fake-a100-mig-0")
	if !ok || d.Model != a100 || *d.TotalSlices != 7 || !*d.MIGEnabled || *d.LastXID != 0 || d.NodeName != "fake-a100-mig" {
		t.Fatalf("GPU 0 = %+v", d)
	}

	ins := e.instances("fake-a100-mig")
	if len(ins) != 7 {
		t.Fatalf("fake-a100-mig has %d instances, want 7", len(ins))
	}
	first := ins[0] // GPU 0, GI 7
	if first.GIID != 7 || first.Profile != "1g.5gb" || first.MIGUUID == nil || *first.MemoryMB != 5120 ||
		first.Health != ledger.HealthOK || *first.ObservedPodName != "train-0" || *first.ObservedContainer != "main" {
		t.Fatalf("GPU 0 GI 7 = %+v", first)
	}

	// 처음 보인 GPU는 재분할 이력을 남기지 않는다.
	if evs := e.events(); len(evs) != 0 {
		t.Fatalf("events after first round = %v", evs)
	}

	// 같은 스냅샷을 다시 반영하면 바뀌는 것이 없다.
	if st := e.collect(fake.Demo()...); st.changed() {
		t.Fatalf("second identical round changed something: %+v", st)
	}
	again := e.instances("fake-a100-mig")
	if again[0].ID != first.ID || !again[0].LastSeenAt.After(first.LastSeenAt) {
		t.Fatalf("second round: id %d→%d, last seen %v→%v", first.ID, again[0].ID, first.LastSeenAt, again[0].LastSeenAt)
	}
}

// ---- 변경 ----

func TestObservedValuesUpdate(t *testing.T) {
	e := newEnv(t)
	snap, gpus := node("n1", 1, 0)
	snap.Instances = fake.Slices(gpus[0], "1g.23gb", 7, 8)
	e.collect(snap)

	// Pod가 조각을 쓰기 시작하고, MIG UUID가 바뀌고, 하드웨어 XID가 났다.
	snap, gpus = node("n1", 1, 0)
	snap.Instances = fake.Slices(gpus[0], "1g.23gb", 7, 8)
	snap.Instances[0] = fake.WithPod(snap.Instances[0], "default", "train-0")
	snap.Instances[0].MIGUUID = "MIG-new"
	snap.LastXID[gpus[0].UUID] = 79
	e.collect(snap)

	ins := e.instances("n1")
	if *ins[0].ObservedPodName != "train-0" || *ins[0].MIGUUID != "MIG-new" {
		t.Fatalf("GI 7 = %+v", ins[0])
	}
	for _, in := range ins {
		if in.Health != ledger.HealthUnhealthy {
			t.Fatalf("XID 79 should make every slice of the GPU unhealthy: %+v", in)
		}
	}
	if d, _ := e.device(gpus[0].UUID); *d.LastXID != 79 {
		t.Fatalf("last XID = %d, want 79", *d.LastXID)
	}

	// Pod가 사라지고, 애플리케이션 계열 XID(43)만 남았다.
	snap, gpus = node("n1", 1, 0)
	snap.Instances = fake.Slices(gpus[0], "1g.23gb", 7, 8)
	snap.LastXID[gpus[0].UUID] = 43
	e.collect(snap)

	ins = e.instances("n1")
	if ins[0].ObservedPodName != nil || ins[0].ObservedPodNamespace != nil || ins[0].Health != ledger.HealthOK {
		t.Fatalf("GI 7 after pod left = %+v", ins[0])
	}
}

// ---- 사라짐 (연속 3회) ----

func TestMissingThreeRoundsDeletes(t *testing.T) {
	e := newEnv(t)
	full := func() metrics.Snapshot {
		s, gpus := node("n1", 2, 0)
		s.Instances = append(fake.Slices(gpus[0], "1g.23gb", 7, 8), fake.Full(gpus[1]))
		return s
	}
	gpu1 := "GPU-fake-n1-1"
	e.collect(full())

	// GPU 1과 GPU 0의 GI 8이 보이지 않는다.
	partial := func() metrics.Snapshot {
		s, gpus := node("n1", 1, 0)
		s.Instances = fake.Slices(gpus[0], "1g.23gb", 7)
		return s
	}
	for missed := int32(1); missed <= 2; missed++ {
		st := e.collect(partial())
		if st.MissedGPUs != 1 || st.MissedInstances != 2 || st.DeletedGPUs != 0 {
			t.Fatalf("missed round %d: %+v", missed, st)
		}
		d, ok := e.device(gpu1)
		if !ok || d.MissedCount != missed {
			t.Fatalf("missed round %d: GPU 1 = %+v (exists %v)", missed, d, ok)
		}
	}
	st := e.collect(partial())
	if st.DeletedGPUs != 1 || st.DeletedInstances != 2 {
		t.Fatalf("third missed round: %+v, want GPU 1 and its slice plus GI 8 deleted", st)
	}
	if _, ok := e.device(gpu1); ok {
		t.Fatal("GPU 1 still exists after 3 missed rounds")
	}
	if ins := e.instances("n1"); len(ins) != 1 || ins[0].GIID != 7 {
		t.Fatalf("instances left = %+v, want only GI 7", ins)
	}
}

func TestReappearingBeforeLimitKeepsRows(t *testing.T) {
	e := newEnv(t)
	full := func() metrics.Snapshot {
		s, gpus := node("n1", 2, 0, 1)
		s.Instances = append(fake.Slices(gpus[0], "1g.23gb", 7, 8), fake.Slices(gpus[1], "1g.23gb", 7, 8)...)
		return s
	}
	e.collect(full())
	before := e.instances("n1")

	// GPU 1이 두 회차 보이지 않았다가 다시 보인다.
	for range 2 {
		s, gpus := node("n1", 1, 0)
		s.Instances = fake.Slices(gpus[0], "1g.23gb", 7, 8)
		e.collect(s)
	}
	st := e.collect(full())
	if st.Reconfigured != 0 || st.MissedGPUs != 0 {
		t.Fatalf("reappear round: %+v", st)
	}
	after := e.instances("n1")
	if len(after) != len(before) {
		t.Fatalf("instances %d → %d", len(before), len(after))
	}
	for i := range after {
		if after[i].ID != before[i].ID || after[i].MissedCount != 0 {
			t.Fatalf("instance %d: id %d→%d, missed %d", i, before[i].ID, after[i].ID, after[i].MissedCount)
		}
	}
	if d, _ := e.device("GPU-fake-n1-1"); d.MissedCount != 0 {
		t.Fatalf("GPU 1 missed count = %d after reappearing", d.MissedCount)
	}
	// 잠시 안 보였던 것은 재분할이 아니다.
	if evs := e.events(); len(evs) != 0 {
		t.Fatalf("events = %v, want none", evs)
	}
}

// ---- 재분할 ----

func TestRepartitionRecordsHistoryOnce(t *testing.T) {
	e := newEnv(t)
	gpu0, gpu1 := "GPU-fake-n1-0", "GPU-fake-n1-1"

	s, gpus := node("n1", 2, 0)
	s.Instances = append(fake.Slices(gpus[0], "1g.23gb", 7, 8, 9, 10, 11, 12, 13), fake.Full(gpus[1]))
	e.collect(s)

	// 관측 할당이 있던 조각이 재분할로 사라져도 수집은 할당 상태를 바꾸지 않는다.
	old := e.instances("n1")[0]
	a, err := e.store.CreateObservedAllocation(t.Context(), ledger.CreateObservedAllocationParams{
		NodeName: "n1", Profile: "1g.23gb", TargetKind: ledger.KindPod,
		TargetNamespace: "default", TargetName: "train-0", InstanceID: &old.ID, AllocatedAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}

	// GPU 0은 2g.45gb × 3으로, GPU 1은 MIG를 켜 1g.23gb × 7로 다시 나눴다.
	repartitioned := func() metrics.Snapshot {
		s, gpus := node("n1", 2, 0, 1)
		s.Instances = append(fake.Slices(gpus[0], "2g.45gb", 3, 4, 5),
			fake.Slices(gpus[1], "1g.23gb", 7, 8, 9, 10, 11, 12, 13)...)
		return s
	}
	st := e.collect(repartitioned())
	if st.Reconfigured != 2 {
		t.Fatalf("repartition round: %+v, want 2 GPUs reconfigured", st)
	}
	want := map[string]string{
		gpu0: "1g.23gb×7 → 2g.45gb×3",
		gpu1: "나누지 않음 → 1g.23gb×7",
	}
	evs := e.events()
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2", len(evs))
	}
	for _, ev := range evs {
		if ev.Event != ledger.EventMIGReconfigured || *ev.NodeName != "n1" || *ev.Message != want[*ev.GPUUUID] {
			t.Errorf("event = %s %s %s %q", ev.Event, *ev.NodeName, *ev.GPUUUID, *ev.Message)
		}
	}

	// 이전 조각은 바로 지우지 않고 연속 3회 미관측 뒤에 지운다. 이력은 다시 남기지 않는다.
	if n := len(e.instances("n1")); n != 8+10 {
		t.Fatalf("instances right after repartition = %d, want 18 (old 8 kept for now)", n)
	}
	e.collect(repartitioned())
	st = e.collect(repartitioned())
	if st.DeletedInstances != 8 {
		t.Fatalf("third round after repartition: %+v, want 8 old instances deleted", st)
	}
	if n := len(e.instances("n1")); n != 10 {
		t.Fatalf("instances after old ones deleted = %d, want 10", n)
	}
	if evs := e.events(); len(evs) != 2 {
		t.Fatalf("events after cleanup = %d, want still 2", len(evs))
	}

	got, err := e.store.GetAllocation(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != ledger.StateAllocated || got.InstanceID != nil {
		t.Fatalf("allocation = state %s instance %v, want ALLOCATED with instance cleared", got.State, got.InstanceID)
	}
}

func TestAllSlicesDestroyedIsReconfiguration(t *testing.T) {
	e := newEnv(t)
	s, gpus := node("n1", 1, 0)
	s.Instances = fake.Slices(gpus[0], "1g.23gb", 7, 8)
	e.collect(s)

	s, _ = node("n1", 1, 0) // MIG는 켜져 있지만 조각이 없다
	if st := e.collect(s); st.Reconfigured != 1 {
		t.Fatalf("stats = %+v, want 1 reconfigured", st)
	}
	if evs := e.events(); len(evs) != 1 || *evs[0].Message != "1g.23gb×2 → 조각 없음" {
		t.Fatalf("events = %v", evs)
	}
}

// ---- 수집 실패 ----

func TestFetchFailureDoesNotCountMisses(t *testing.T) {
	e := newEnv(t)
	e.collect(twoNodes()...)
	boom := errors.New("dcgm-exporter unreachable")

	// 아무 노드도 읽지 못했다: 장부를 건드리지 않는다.
	e.src.Set()
	e.src.SetError(boom)
	st, err := e.c.RunOnce(t.Context())
	if !errors.Is(err, boom) || st.Nodes != 0 {
		t.Fatalf("RunOnce = %+v, %v", st, err)
	}

	// node-b만 읽었다: node-a는 미관측으로 세지 않는다.
	e.src.Set(twoNodes()[1])
	st, err = e.c.RunOnce(t.Context())
	if !errors.Is(err, boom) || st.Nodes != 1 || st.MissedGPUs != 0 {
		t.Fatalf("partial RunOnce = %+v, %v", st, err)
	}
	for _, in := range e.instances("node-a") {
		if in.MissedCount != 0 {
			t.Fatalf("unread node counted as missed: %+v", in)
		}
	}

	// 노드는 응답했는데 GPU가 하나도 없으면 미관측으로 센다.
	e.src.SetError(nil)
	st = e.collect(metrics.Snapshot{Node: "node-b"})
	if st.MissedGPUs != 2 || st.MissedInstances != 2 {
		t.Fatalf("empty snapshot round = %+v", st)
	}
}

func TestInstanceWithoutGPUIsSkipped(t *testing.T) {
	e := newEnv(t)
	s, gpus := node("n1", 1, 0)
	orphan := fake.GPU("other", 0, b200, 183359, true)
	s.Instances = append(fake.Slices(gpus[0], "1g.23gb", 7), fake.Slices(orphan, "1g.23gb", 7)...)

	st := e.collect(s)
	if st.Skipped != 1 || st.Instances != 1 {
		t.Fatalf("stats = %+v, want 1 written and 1 skipped", st)
	}
}

func TestSnapshotWithoutNodeIsAnError(t *testing.T) {
	e := newEnv(t)
	e.src.Set(metrics.Snapshot{At: t0}, fake.Demo()[0])
	st, err := e.c.RunOnce(t.Context())
	if err == nil || st.Nodes != 1 {
		t.Fatalf("RunOnce = %+v, %v; want the named node written and an error for the other", st, err)
	}
}
