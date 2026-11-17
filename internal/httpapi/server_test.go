package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/yjp8842/gpu-pool-manager/internal/allocation"
	"github.com/yjp8842/gpu-pool-manager/internal/collector"
	"github.com/yjp8842/gpu-pool-manager/internal/httpapi"
	"github.com/yjp8842/gpu-pool-manager/internal/kube"
	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
	"github.com/yjp8842/gpu-pool-manager/internal/metrics/fake"
	"github.com/yjp8842/gpu-pool-manager/internal/testpg"
)

func TestMain(m *testing.M) {
	os.Exit(testpg.Main(m, ledger.Migrate))
}

var t0 = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

// api는 테스트 서버와, 응답을 명세로 검사하는 라우터다.
type api struct {
	t      *testing.T
	srv    *httptest.Server
	router routers.Router
	store  *ledger.Store
	alloc  *allocation.Service
}

func newAPI(t *testing.T) *api {
	t.Helper()
	store, err := ledger.Open(t.Context(), testpg.NewDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	spec, err := httpapi.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	alloc := allocation.New(store, allocation.Config{TTL: 5 * time.Minute})
	srv := httptest.NewServer(httpapi.NewHandler(store, alloc, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return &api{t: t, srv: srv, router: router, store: store, alloc: alloc}
}

// get은 path를 GET으로 요청한다 (do 참고).
func (a *api) get(path string, wantStatus int, out any) {
	a.t.Helper()
	a.do(http.MethodGet, path, nil, wantStatus, out)
}

// do는 요청을 보내고, 응답이 명세(상태 코드, Content-Type, 본문 스키마)와 맞는지 검사한 뒤 본문을 out에 읽는다.
// in이 nil이 아니면 JSON 본문으로 보낸다.
func (a *api) do(method, path string, in any, wantStatus int, out any) {
	a.t.Helper()
	var reqBody io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			a.t.Fatal(err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(a.t.Context(), method, a.srv.URL+path, reqBody)
	if err != nil {
		a.t.Fatal(err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		a.t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		a.t.Fatalf("%s %s = %d %s, want %d", method, path, resp.StatusCode, body, wantStatus)
	}

	route, params, err := a.router.FindRoute(req)
	if err != nil {
		a.t.Fatalf("%s %s: route not in spec: %v", method, path, err)
	}
	err = openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
		Status:                 resp.StatusCode,
		Header:                 resp.Header,
		Body:                   io.NopCloser(bytes.NewReader(body)),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	})
	if err != nil {
		a.t.Fatalf("%s %s: response does not match spec: %v\n%s", method, path, err, body)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			a.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
}

// seed는 장부에 화면 하나를 채울 만큼의 데이터를 넣는다 (fake.Demo의 데모 구성 위에).
// fake-a100-mig는 A100 1장을 1g.5gb × 7로 나눈 노드다. GI 7은 train-0이 쓰고 관측 할당이 있고,
// 1g.5gb 예약 하나가 열려 있다. 그래서 빈 조각 6, 남은 수량 5다.
func seed(t *testing.T, store *ledger.Store) {
	t.Helper()
	ctx := t.Context()
	seedMetrics(t, store)

	gi7 := findInstance(t, store, "fake-a100-mig", 0, 7)
	if _, err := store.CreateObservedAllocation(ctx, ledger.CreateObservedAllocationParams{
		NodeName: "fake-a100-mig", Profile: "1g.5gb", TargetKind: ledger.KindPod,
		TargetNamespace: "default", TargetName: "train-0", InstanceID: &gi7, AllocatedAt: t0,
	}); err != nil {
		t.Fatal(err)
	}
	r, err := store.CreateReservation(ctx, ledger.CreateReservationParams{
		RequestID: "req-1", NodeName: "fake-a100-mig", Profile: "1g.5gb", TargetKind: ledger.KindPod,
		TargetNamespace: "default", TargetName: "train-9", Requester: "tester",
		ReservedAt: t0, ExpiresAt: t0.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	for i, ev := range []ledger.AppendEventParams{
		{OccurredAt: t0, Event: ledger.EventMIGReconfigured, NodeName: new("fake-a100-mig"), GPUUUID: new("GPU-fake-fake-a100-mig-0"),
			Message: new("나누지 않음 → 1g.5gb×7")},
		{OccurredAt: t0.Add(time.Minute), Event: ledger.EventReserved, NodeName: new("fake-a100-mig"), Profile: new("1g.5gb"),
			WorkloadKind: new(ledger.KindPod), WorkloadNamespace: new("default"), WorkloadName: new("train-9"),
			Requester: new("tester"), RequestID: new("req-1"), AllocationID: &r.ID},
	} {
		if _, err := store.AppendEvent(ctx, ev); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
}

// seedMetrics는 목업 메트릭(fake.Demo)과 K8s 상태만 넣는다. 할당·예약·이력은 없어서
// fake-a100-mig의 1g.5gb 조각 7개가 모두 비어 있다(train-0, train-1은 관측만 됐다).
func seedMetrics(t *testing.T, store *ledger.Store) {
	t.Helper()
	ctx := t.Context()

	c := collect(t, store, t0, 0)
	if _, err := c.SyncKube(ctx, kube.Snapshot{
		Nodes: []kube.Node{{Name: "fake-a100-mig", Ready: true, InstanceType: "a2-highgpu-1g", HasGPU: true}},
		Workloads: []kube.Workload{
			{Namespace: "default", Name: "train-0", UID: "u1", NodeName: "fake-a100-mig", Phase: "Running"},
			{Namespace: "default", Name: "train-1", UID: "u2", NodeName: "fake-a100-mig", Phase: "Running"},
			{Namespace: "default", Name: "pending-0", UID: "u4", Phase: "Pending"},
		},
	}, t0); err != nil {
		t.Fatal(err)
	}
}

// breakGPU는 fake-a100-mig의 GPU에 XID 79(하드웨어 오류)를 넣고 한 번 더 수집한다. 조각 7개가 모두 비정상이 된다.
func breakGPU(t *testing.T, store *ledger.Store) {
	t.Helper()
	collect(t, store, t0.Add(time.Minute), 79)
}

// collect는 fake.Demo를 시각 at, 마지막 XID xid로 한 번 수집한다.
func collect(t *testing.T, store *ledger.Store, at time.Time, xid int) *collector.Collector {
	t.Helper()
	demo := fake.Demo()
	for i := range demo {
		demo[i].At = at
		for _, g := range demo[i].GPUs {
			demo[i].LastXID[g.UUID] = xid
		}
	}
	c := collector.New(fake.New(demo...), store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := c.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	return c
}

func findInstance(t *testing.T, store *ledger.Store, node string, gpuIndex, gi int32) int64 {
	t.Helper()
	rows, err := store.ListGPUInstances(t.Context(), ledger.ListGPUInstancesParams{NodeName: &node})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.GPUIndex == gpuIndex && r.GPUInstance.GIID == gi {
			return r.GPUInstance.ID
		}
	}
	t.Fatalf("instance %s/%d/%d not found", node, gpuIndex, gi)
	return 0
}

func TestHealthz(t *testing.T) {
	a := newAPI(t)
	var h httpapi.Health
	a.get("/healthz", http.StatusOK, &h)
	if h.Status != "ok" {
		t.Fatalf("status = %q", h.Status)
	}
}

func TestEmptyLedgerReturnsEmptyLists(t *testing.T) {
	a := newAPI(t)
	for _, path := range []string{
		"/api/v1/nodes", "/api/v1/gpus", "/api/v1/instances", "/api/v1/capacity", "/api/v1/workloads", "/api/v1/events",
		"/api/v1/reservations",
	} {
		var out struct{ Items []json.RawMessage }
		a.get(path, http.StatusOK, &out)
		if out.Items == nil || len(out.Items) != 0 {
			t.Errorf("GET %s items = %v, want []", path, out.Items)
		}
	}
}

func TestNodes(t *testing.T) {
	a := newAPI(t)
	seed(t, a.store)

	var out httpapi.NodeList
	a.get("/api/v1/nodes", http.StatusOK, &out)
	if len(out.Items) != 1 {
		t.Fatalf("nodes = %+v", out.Items)
	}
	m := out.Items[0]
	if m.Name != "fake-a100-mig" || !*m.Ready || *m.InstanceType != "a2-highgpu-1g" ||
		m.GpuCount != 1 || m.InstanceCount != 7 || m.FreeInstanceCount != 6 {
		t.Fatalf("fake-a100-mig = %+v", m)
	}

	// GPU가 고장 나면(XID 79) 빈 조각이 없다.
	breakGPU(t, a.store)
	a.get("/api/v1/nodes", http.StatusOK, &out)
	if m := out.Items[0]; m.InstanceCount != 7 || m.FreeInstanceCount != 0 {
		t.Fatalf("after XID 79 = %+v, want 7 instances and none free", m)
	}
}

func TestGPUs(t *testing.T) {
	a := newAPI(t)
	seed(t, a.store)

	var out httpapi.GPUList
	a.get("/api/v1/gpus?node=fake-a100-mig", http.StatusOK, &out)
	if len(out.Items) != 1 {
		t.Fatalf("gpus = %d, want 1", len(out.Items))
	}
	g0 := out.Items[0]
	if g0.Index != 0 || !*g0.MigEnabled || *g0.TotalSlices != 7 || len(g0.Instances) != 7 {
		t.Fatalf("GPU 0 = %+v", g0)
	}
	gi7 := g0.Instances[0]
	if gi7.GiID != 7 || gi7.State != httpapi.InstanceStateALLOCATED || gi7.ObservedPod.Name != "train-0" ||
		gi7.Allocation.Source != httpapi.OBSERVED || gi7.Allocation.Target.Name != "train-0" {
		t.Fatalf("GPU 0 GI 7 = %+v", gi7)
	}
}

func TestInstances(t *testing.T) {
	a := newAPI(t)
	seed(t, a.store)

	var out httpapi.InstanceList
	a.get("/api/v1/instances?node=fake-a100-mig&profile=1g.5gb&state=FREE", http.StatusOK, &out)
	if len(out.Items) != 6 {
		t.Fatalf("free 1g.5gb on fake-a100-mig = %d, want 6", len(out.Items))
	}
	a.get("/api/v1/instances?state=ALLOCATED", http.StatusOK, &out)
	if len(out.Items) != 1 {
		t.Fatalf("allocated = %d, want 1", len(out.Items))
	}
	a.get("/api/v1/instances?state=UNHEALTHY", http.StatusOK, &out)
	if len(out.Items) != 0 {
		t.Fatalf("unhealthy before XID = %+v", out.Items)
	}

	// XID 79가 나면 할당이 없는 조각 6개는 UNHEALTHY다. 할당된 GI 7은 ALLOCATED로 남는다.
	breakGPU(t, a.store)
	a.get("/api/v1/instances?state=UNHEALTHY", http.StatusOK, &out)
	if len(out.Items) != 6 || out.Items[0].Node != "fake-a100-mig" || out.Items[0].Health != httpapi.InstanceHealthUNHEALTHY {
		t.Fatalf("unhealthy = %+v", out.Items)
	}

	var e httpapi.Error
	a.get("/api/v1/instances?state=BUSY", http.StatusBadRequest, &e)
	if e.Code != "bad_request" {
		t.Fatalf("error = %+v", e)
	}
}

func TestCapacity(t *testing.T) {
	a := newAPI(t)
	seed(t, a.store)

	var out httpapi.CapacityList
	a.get("/api/v1/capacity", http.StatusOK, &out)
	// GKE 방식: 자원 이름은 nvidia.com/gpu이고, 조각 크기는 노드 라벨로 고른다.
	want := []httpapi.Capacity{
		// 7개 중 1개 할당, 1개 예약 → 남은 수량 5
		{Node: "fake-a100-mig", Profile: "1g.5gb", ResourceName: "nvidia.com/gpu",
			NodeSelector: httpapi.NodeSelector{"cloud.google.com/gke-gpu-partition-size": "1g.5gb"},
			Total:        7, Free: 6, Reserved: 1, Available: 5},
	}
	if len(out.Items) != len(want) {
		t.Fatalf("capacity = %+v", out.Items)
	}
	for i := range want {
		if !reflect.DeepEqual(out.Items[i], want[i]) {
			t.Errorf("capacity %d = %+v, want %+v", i, out.Items[i], want[i])
		}
	}

	a.get("/api/v1/capacity?node=fake-a100-mig&profile=full", http.StatusOK, &out)
	if len(out.Items) != 0 {
		t.Fatalf("full capacity on fake-a100-mig = %+v, want none (every GPU is split)", out.Items)
	}
}

func TestWorkloads(t *testing.T) {
	a := newAPI(t)
	seed(t, a.store)

	var out httpapi.WorkloadList
	a.get("/api/v1/workloads", http.StatusOK, &out)
	if len(out.Items) != 3 {
		t.Fatalf("workloads = %d, want 3", len(out.Items))
	}
	byName := map[string]httpapi.Workload{}
	for _, w := range out.Items {
		byName[w.Name] = w
	}
	// train-0은 관측 Pod이면서 할당 대상이다. 같은 조각은 한 번만 나온다.
	if w := byName["train-0"]; len(w.Instances) != 1 || w.Instances[0].GiID != 7 || w.Instances[0].GpuIndex != 0 {
		t.Fatalf("train-0 = %+v", w)
	}
	// train-1은 관측만 되고 할당은 아직 없다(정합 작업 전).
	if w := byName["train-1"]; len(w.Instances) != 1 || w.Instances[0].GiID != 8 || w.Instances[0].Profile != "1g.5gb" {
		t.Fatalf("train-1 = %+v", w)
	}
	if w := byName["pending-0"]; w.Node != nil || len(w.Instances) != 0 || w.Kind != httpapi.POD {
		t.Fatalf("pending-0 = %+v", w)
	}
}

func TestEventsPaging(t *testing.T) {
	a := newAPI(t)
	seed(t, a.store)

	var page httpapi.EventList
	a.get("/api/v1/events?limit=1", http.StatusOK, &page)
	if len(page.Items) != 1 || page.NextBefore == nil {
		t.Fatalf("first page = %+v", page)
	}
	if page.Items[0].Event != httpapi.EventTypeRESERVED || page.Items[0].AllocationID == nil ||
		*page.Items[0].RequestID != "req-1" || page.Items[0].Workload.Name != "train-9" {
		t.Fatalf("newest event = %+v", page.Items[0])
	}

	var next httpapi.EventList
	a.get("/api/v1/events?limit=1&before="+strconv.FormatInt(*page.NextBefore, 10), http.StatusOK, &next)
	if len(next.Items) != 1 || next.NextBefore != nil || next.Items[0].Event != httpapi.EventTypeMIGRECONFIGURED {
		t.Fatalf("second page = %+v", next)
	}

	a.get("/api/v1/events?limit=0", http.StatusBadRequest, nil)
	a.get("/api/v1/events?limit=many", http.StatusBadRequest, nil)
}

// 명세 검사가 실제로 틀린 응답을 잡는지 확인한다. 검사가 늘 통과하면 위 테스트들이 의미가 없다.
func TestSpecCheckRejectsMismatch(t *testing.T) {
	spec, err := httpapi.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	route, params, err := router.FindRoute(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"items": null}`,                // 배열은 null이면 안 된다
		`{"items": [{"name": "n1"}]}`,    // 필수 항목이 빠졌다
		`{"items": [], "unexpected": 1}`, // 허용하지만 상태 코드를 바꿔 본다 (아래)
	} {
		status := http.StatusOK
		if body == `{"items": [], "unexpected": 1}` {
			status = http.StatusTeapot // 명세에 없는 상태 코드
		}
		err := openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
			RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
			Status:                 status,
			Header:                 http.Header{"Content-Type": []string{"application/json"}},
			Body:                   io.NopCloser(bytes.NewReader([]byte(body))),
			Options:                &openapi3filter.Options{IncludeResponseStatus: true},
		})
		if err == nil {
			t.Errorf("status %d body %s passed the spec check", status, body)
		}
	}
}
