package allocation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
	"github.com/yjp8842/gpu-pool-manager/internal/testpg"
)

func TestMain(m *testing.M) {
	os.Exit(testpg.Main(m, ledger.Migrate))
}

var t0 = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

const (
	node    = "node-a"
	profile = "1g.23gb"
	gpu0    = "GPU-a0"
)

type env struct {
	t     *testing.T
	store *ledger.Store
	svc   *Service
	now   time.Time
	mu    sync.Mutex
}

// newEnv는 node-a의 GPU 0을 1g.23gb × n으로 나눈 장부와 Service를 만든다. GI ID는 7부터다.
func newEnv(t *testing.T, n int) *env {
	t.Helper()
	store, err := ledger.Open(t.Context(), testpg.NewDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	e := &env{t: t, store: store, now: t0}
	e.svc = New(store, Config{TTL: 5 * time.Minute, Now: e.clock})

	if err := store.EnsureNode(t.Context(), ledger.EnsureNodeParams{Name: node, LastSeenAt: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertGPUDevice(t.Context(), ledger.UpsertGPUDeviceParams{
		UUID: gpu0, NodeName: node, Model: "NVIDIA B200", MIGEnabled: new(true), LastSeenAt: t0,
	}); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		e.slice(int32(7+i), "", "")
	}
	return e
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
	return e.now
}

// slice는 GPU 0의 GI 조각을 관측 값으로 넣는다. podName이 비어 있으면 아무도 쓰지 않는다.
func (e *env) slice(gi int32, podNamespace, podName string) ledger.GPUInstance {
	e.t.Helper()
	p := ledger.UpsertGPUInstanceParams{
		ParentUUID: gpu0, GIID: gi, Profile: profile, MIGUUID: new(fmt.Sprintf("MIG-a0-%d", gi)),
		Health: ledger.HealthOK, LastSeenAt: t0,
	}
	if podName != "" {
		p.ObservedPodNamespace, p.ObservedPodName = &podNamespace, &podName
	}
	in, err := e.store.UpsertGPUInstance(e.t.Context(), p)
	if err != nil {
		e.t.Fatal(err)
	}
	return in
}

func (e *env) reserve(requestID, pod string) (ledger.Allocation, error) {
	a, _, err := e.svc.Reserve(e.t.Context(), ReserveRequest{
		RequestID: requestID, Requester: "tester", Node: node, Profile: profile,
		PodNamespace: "default", PodName: pod,
	})
	return a, err
}

func (e *env) mustReserve(requestID, pod string) ledger.Allocation {
	e.t.Helper()
	a, err := e.reserve(requestID, pod)
	if err != nil {
		e.t.Fatalf("reserve %s: %v", requestID, err)
	}
	return a
}

func (e *env) reconcile() ReconcileStats {
	e.t.Helper()
	st, err := e.svc.Reconcile(e.t.Context(), e.clock())
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *env) capacity() ledger.GetCapacityRow {
	e.t.Helper()
	c, err := e.store.GetCapacity(e.t.Context(), ledger.GetCapacityParams{NodeName: node, Profile: profile})
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) get(id uuid.UUID) ledger.Allocation {
	e.t.Helper()
	a, err := e.svc.Get(e.t.Context(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

// events는 이력 이벤트 이름을 오래된 것부터 센다.
func (e *env) events() map[string]int {
	e.t.Helper()
	evs, err := e.store.ListEvents(e.t.Context(), ledger.ListEventsParams{RowLimit: 1000})
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]int{}
	for _, ev := range evs {
		out[ev.Event]++
	}
	return out
}

func (e *env) eventsOf(id uuid.UUID) []ledger.AllocationEvent {
	e.t.Helper()
	evs, err := e.store.ListEventsByAllocation(e.t.Context(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

// ---- 예약 ----

func TestReserveUntilNoCapacity(t *testing.T) {
	e := newEnv(t, 2)
	a := e.mustReserve("req-1", "train-1")
	if a.State != ledger.StateReserved || !a.ExpiresAt.Equal(t0.Add(5*time.Minute)) || a.InstanceID != nil {
		t.Fatalf("reservation = %+v", a)
	}
	e.mustReserve("req-2", "train-2")

	_, err := e.reserve("req-3", "train-3")
	if !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("third reservation err = %v, want ErrNoCapacity", err)
	}
	if c := e.capacity(); c.FreeCount != 2 || c.ReservedCount != 2 {
		t.Fatalf("capacity = %+v", c)
	}
	if ev := e.events(); ev[ledger.EventReserved] != 2 || ev[ledger.EventRejected] != 1 {
		t.Fatalf("events = %v, want 2 RESERVED and 1 REJECTED", ev)
	}
	// 거부된 요청 ID는 장부에 남지 않으므로, 자리가 나면 같은 ID로 다시 예약할 수 있다.
	if _, err := e.store.GetAllocationByRequestID(t.Context(), "req-3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected request stored: %v", err)
	}
}

// 완료 기준: 동시 예약 100건에서 남은 수량을 넘는 예약이 0건이다 (설계도 개발 단계 5).
func TestConcurrentReservationsNeverExceedCapacity(t *testing.T) {
	const free, requests = 7, 100
	e := newEnv(t, free)

	var (
		wg               sync.WaitGroup
		mu               sync.Mutex
		ok, full, failed int
	)
	for i := range requests {
		wg.Go(func() {
			_, err := e.reserve(fmt.Sprintf("req-%03d", i), fmt.Sprintf("train-%03d", i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrNoCapacity):
				full++
			default:
				failed++
				t.Errorf("request %d: %v", i, err)
			}
		})
	}
	wg.Wait()

	if ok != free || full != requests-free || failed != 0 {
		t.Fatalf("ok %d, no capacity %d, failed %d; want %d, %d, 0", ok, full, failed, free, requests-free)
	}
	if c := e.capacity(); c.ReservedCount != free {
		t.Fatalf("reserved in ledger = %d, want %d", c.ReservedCount, free)
	}
	if ev := e.events(); ev[ledger.EventReserved] != free || ev[ledger.EventRejected] != requests-free {
		t.Fatalf("events = %v", ev)
	}
}

func TestReserveIsIdempotentPerRequestID(t *testing.T) {
	e := newEnv(t, 2)
	first := e.mustReserve("req-1", "train-1")

	again, created, err := e.svc.Reserve(t.Context(), ReserveRequest{
		RequestID: "req-1", Requester: "tester", Node: node, Profile: profile, PodNamespace: "default", PodName: "train-1",
	})
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay = %s created %v err %v, want %s not created", again.ID, created, err, first.ID)
	}
	if _, err := e.reserve("req-1", "train-other"); !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("same request id, different pod: err = %v, want ErrRequestIDConflict", err)
	}
	if c := e.capacity(); c.ReservedCount != 1 {
		t.Fatalf("reserved = %d, want 1", c.ReservedCount)
	}
}

func TestReserveValidates(t *testing.T) {
	e := newEnv(t, 1)
	_, _, err := e.svc.Reserve(t.Context(), ReserveRequest{RequestID: "req-1", Node: node, Profile: profile, PodName: "p"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

// ---- 정합 작업 ----

func TestReconcileBindsAndReleases(t *testing.T) {
	e := newEnv(t, 2)
	r := e.mustReserve("req-1", "train-1")

	// 대상 Pod가 아직 뜨지 않았다.
	if st := e.reconcile(); st.changed() {
		t.Fatalf("nothing observed yet, but %+v", st)
	}

	// 서버1이 GI 8에서 train-1을 관측했다.
	gi8 := e.slice(8, "default", "train-1")
	e.advance(time.Minute)
	if st := e.reconcile(); st.Bound != 1 || st.Observed != 0 {
		t.Fatalf("stats = %+v, want 1 bound", st)
	}
	bound := e.get(r.ID)
	if bound.State != ledger.StateAllocated || *bound.InstanceID != gi8.ID {
		t.Fatalf("bound = %+v", bound)
	}
	if c := e.capacity(); c.FreeCount != 1 || c.ReservedCount != 0 {
		t.Fatalf("capacity after bind = %+v", c)
	}
	// 같은 상태로 다시 돌리면 바뀌는 것이 없다.
	if st := e.reconcile(); st.changed() {
		t.Fatalf("second reconcile changed %+v", st)
	}

	// Pod가 끝나 조각에서 사라졌다.
	e.slice(8, "", "")
	e.advance(time.Minute)
	if st := e.reconcile(); st.Released != 1 {
		t.Fatalf("stats = %+v, want 1 released", st)
	}
	if a := e.get(r.ID); a.State != ledger.StateReleased {
		t.Fatalf("after pod left = %+v", a)
	}
	if c := e.capacity(); c.FreeCount != 2 {
		t.Fatalf("capacity after release = %+v", c)
	}

	// 이력에는 예약 → 확정(실제 조각) → 반납이 남는다.
	evs := e.eventsOf(r.ID)
	if len(evs) != 3 || evs[0].Event != ledger.EventReserved || evs[1].Event != ledger.EventAllocated || evs[2].Event != ledger.EventReleased {
		t.Fatalf("events = %v", names(evs))
	}
	if evs[1].GPUUUID == nil || *evs[1].GPUUUID != gpu0 || *evs[1].GIID != 8 || *evs[1].MIGUUID != "MIG-a0-8" {
		t.Fatalf("allocated event = %+v", evs[1])
	}
}

func TestReconcileCreatesObservedAllocation(t *testing.T) {
	e := newEnv(t, 3)
	r := e.mustReserve("req-1", "train-1")

	// 예약 없이 K8s에서 바로 만든 Pod가 GI 7을 쓴다 (관측 할당).
	e.slice(7, "default", "direct-0")
	st := e.reconcile()
	if st.Observed != 1 || st.Bound != 0 {
		t.Fatalf("stats = %+v, want 1 observed", st)
	}
	views, err := e.store.ListInstanceViews(t.Context(), ledger.ListInstanceViewsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if v := views[0]; v.AllocationSource == nil || *v.AllocationSource != ledger.SourceObserved || *v.AllocationTargetName != "direct-0" {
		t.Fatalf("GI 7 = %+v", v)
	}
	// 남은 수량 = 빈 조각 2 − 예약 1 = 1
	if c := e.capacity(); c.FreeCount != 2 || c.ReservedCount != 1 {
		t.Fatalf("capacity = %+v", c)
	}
	if a := e.get(r.ID); a.State != ledger.StateReserved {
		t.Fatalf("reservation = %+v, want still RESERVED", a)
	}
	if st := e.reconcile(); st.changed() {
		t.Fatalf("second reconcile changed %+v", st)
	}
}

func TestReconcileReleasesWhenInstanceDisappears(t *testing.T) {
	e := newEnv(t, 1)
	in := e.slice(7, "default", "direct-0")
	e.reconcile()

	// MIG를 다시 나눠 조각이 지워졌다 (서버1이 3회 미관측 뒤 지운다).
	if _, err := e.store.DeleteGPUInstance(t.Context(), in.ID); err != nil {
		t.Fatal(err)
	}
	if st := e.reconcile(); st.Released != 1 {
		t.Fatalf("stats = %+v, want 1 released", st)
	}
	if ev := e.events(); ev[ledger.EventReleased] != 1 {
		t.Fatalf("events = %v", ev)
	}
}

func TestReconcilePodOnAnotherProfileDoesNotBind(t *testing.T) {
	e := newEnv(t, 1)
	r := e.mustReserve("req-1", "train-1")
	// 예약은 1g.23gb인데 train-1이 다른 프로파일 조각에서 보였다 (설계도 10장 12번).
	if _, err := e.store.UpsertGPUInstance(t.Context(), ledger.UpsertGPUInstanceParams{
		ParentUUID: gpu0, GIID: 3, Profile: "2g.45gb", Health: ledger.HealthOK,
		ObservedPodNamespace: new("default"), ObservedPodName: new("train-1"), LastSeenAt: t0,
	}); err != nil {
		t.Fatal(err)
	}
	if st := e.reconcile(); st.Bound != 0 || st.Observed != 1 {
		t.Fatalf("stats = %+v, want observed allocation and no bind", st)
	}
	if a := e.get(r.ID); a.State != ledger.StateReserved {
		t.Fatalf("reservation = %+v, want RESERVED until it expires", a)
	}
}

func TestReconcileExpires(t *testing.T) {
	e := newEnv(t, 1)
	r := e.mustReserve("req-1", "train-1")

	e.advance(4 * time.Minute)
	if st := e.reconcile(); st.Expired != 0 {
		t.Fatalf("expired before TTL: %+v", st)
	}
	e.advance(2 * time.Minute) // 6분: 만료 시간(5분)이 지났다
	if st := e.reconcile(); st.Expired != 1 {
		t.Fatalf("stats = %+v, want 1 expired", st)
	}
	a := e.get(r.ID)
	if a.State != ledger.StateExpired || !a.EndedAt.Equal(t0.Add(6*time.Minute)) {
		t.Fatalf("expired = %+v", a)
	}
	if c := e.capacity(); c.ReservedCount != 0 || c.FreeCount != 1 {
		t.Fatalf("capacity after expiry = %+v", c)
	}
	evs := e.eventsOf(r.ID)
	if len(evs) != 2 || evs[1].Event != ledger.EventExpired {
		t.Fatalf("events = %v", names(evs))
	}
	// 만료로 자리가 났으니 다시 예약할 수 있다.
	e.mustReserve("req-2", "train-2")
}

func TestReconcileRunsOnceAtATime(t *testing.T) {
	e := newEnv(t, 1)
	// 다른 서버2가 정합 작업 중인 것처럼 잠금을 쥔다.
	hold := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = e.store.InTx(context.Background(), func(q *ledger.Queries) error {
			if ok, err := q.TryLockReconcile(context.Background(), reconcileLockKey); err != nil || !ok {
				t.Errorf("lock: %v %v", ok, err)
			}
			close(held)
			<-hold
			return nil
		})
	}()
	<-held
	st := e.reconcile()
	close(hold)
	if !st.Skipped {
		t.Fatalf("stats = %+v, want skipped while another reconcile holds the lock", st)
	}
}

// ---- 취소·반납 ----

func TestCancel(t *testing.T) {
	e := newEnv(t, 2)
	r := e.mustReserve("req-1", "train-1")

	a, err := e.svc.Cancel(t.Context(), r.ID)
	if err != nil || a.State != ledger.StateCancelled {
		t.Fatalf("cancel reserved = %+v, %v", a, err)
	}
	// 이미 닫힌 것은 그대로 돌려주고 이력을 더 남기지 않는다.
	if a, err := e.svc.Cancel(t.Context(), r.ID); err != nil || a.State != ledger.StateCancelled {
		t.Fatalf("cancel again = %+v, %v", a, err)
	}
	if evs := e.eventsOf(r.ID); len(evs) != 2 || evs[1].Event != ledger.EventCancelled {
		t.Fatalf("events = %v", names(evs))
	}

	// 확정된 할당을 반납하면 RELEASED. Pod가 계속 쓰고 있으면 다음 정합 작업이 관측 할당으로 다시 잡는다.
	r2 := e.mustReserve("req-2", "train-2")
	e.slice(7, "default", "train-2")
	e.reconcile()
	a, err = e.svc.Cancel(t.Context(), r2.ID)
	if err != nil || a.State != ledger.StateReleased {
		t.Fatalf("release allocated = %+v, %v", a, err)
	}
	if evs := e.eventsOf(r2.ID); evs[len(evs)-1].Event != ledger.EventReleased || *evs[len(evs)-1].GIID != 7 {
		t.Fatalf("events = %v", names(evs))
	}
	if st := e.reconcile(); st.Observed != 1 {
		t.Fatalf("stats = %+v, want the still-running pod observed again", st)
	}

	if _, err := e.svc.Cancel(t.Context(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancel unknown = %v, want ErrNotFound", err)
	}
}

// ---- 용량 부족 ----

func TestShortReservations(t *testing.T) {
	e := newEnv(t, 2)
	first := e.mustReserve("req-1", "train-1")
	e.advance(time.Second)
	second := e.mustReserve("req-2", "train-2")

	short, err := e.svc.ShortReservations(t.Context())
	if err != nil || len(short) != 0 {
		t.Fatalf("short = %v, %v; want none", short, err)
	}

	// 예약 없는 Pod가 빈 조각 하나를 가져가 빈 조각(1)이 예약(2)보다 적어졌다. 늦게 만든 예약이 용량 부족이다.
	e.slice(7, "default", "direct-0")
	e.reconcile()
	short, err = e.svc.ShortReservations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if short[first.ID] || !short[second.ID] {
		t.Fatalf("short = %v, want only the later reservation %s", short, second.ID)
	}
}

func names(evs []ledger.AllocationEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Event
	}
	return out
}
