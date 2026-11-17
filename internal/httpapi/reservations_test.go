package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/yjp8842/gpu-pool-manager/internal/httpapi"
	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
)

func reservationRequest(requestID, node, profile, pod string) httpapi.ReservationRequest {
	return httpapi.ReservationRequest{
		RequestID: requestID, Requester: "tester", Node: node, Profile: profile,
		Target: httpapi.WorkloadRef{Kind: httpapi.POD, Namespace: "default", Name: pod},
	}
}

func TestReservationLifecycleOverHTTP(t *testing.T) {
	a := newAPI(t)
	seed(t, a.store) // fake-a100-mig 1g.5gb: 빈 조각 6, 예약 1

	// 예약
	var r httpapi.Reservation
	a.do(http.MethodPost, "/api/v1/reservations", reservationRequest("req-api-1", "fake-a100-mig", "1g.5gb", "train-5"), http.StatusCreated, &r)
	if r.State != httpapi.AllocationStateRESERVED || r.Source != httpapi.RESERVATION || r.ResourceName != "nvidia.com/gpu" ||
		r.NodeSelector["cloud.google.com/gke-gpu-partition-size"] != "1g.5gb" || len(r.NodeSelector) != 1 ||
		r.ExpiresAt == nil || r.Instance != nil || r.CapacityShort || *r.RequestID != "req-api-1" {
		t.Fatalf("created = %+v", r)
	}

	// 같은 요청 ID로 다시 보내면 처음 예약을 200으로 돌려준다.
	var again httpapi.Reservation
	a.do(http.MethodPost, "/api/v1/reservations", reservationRequest("req-api-1", "fake-a100-mig", "1g.5gb", "train-5"), http.StatusOK, &again)
	if again.ID != r.ID {
		t.Fatalf("replay id = %s, want %s", again.ID, r.ID)
	}
	var e httpapi.Error
	a.do(http.MethodPost, "/api/v1/reservations", reservationRequest("req-api-1", "fake-a100-mig", "1g.5gb", "other"), http.StatusConflict, &e)
	if e.Code != "request_id_conflict" {
		t.Fatalf("conflict = %+v", e)
	}

	var c httpapi.CapacityList
	a.get("/api/v1/capacity?node=fake-a100-mig&profile=1g.5gb", http.StatusOK, &c)
	if c.Items[0].Reserved != 2 || c.Items[0].Available != 4 {
		t.Fatalf("capacity = %+v", c.Items[0])
	}

	// 진행 중인 예약 목록: 시드의 req-1과 방금 만든 req-api-1. 관측 할당은 들어가지 않는다.
	var list httpapi.ReservationList
	a.get("/api/v1/reservations", http.StatusOK, &list)
	if len(list.Items) != 2 || list.Items[0].ID != r.ID {
		t.Fatalf("open reservations = %+v", list.Items)
	}

	// 서버1이 GI 8에서 train-5를 관측하고, 정합 작업이 예약을 그 조각에 묶는다.
	if _, err := a.store.UpsertGPUInstance(t.Context(), ledger.UpsertGPUInstanceParams{
		ParentUUID: "GPU-fake-fake-a100-mig-0", GIID: 8, Profile: "1g.5gb", Health: ledger.HealthOK,
		ObservedPodNamespace: new("default"), ObservedPodName: new("train-5"), LastSeenAt: t0,
	}); err != nil {
		t.Fatal(err)
	}
	if st, err := a.alloc.Reconcile(t.Context(), time.Now()); err != nil || st.Bound != 1 {
		t.Fatalf("reconcile = %+v, %v", st, err)
	}
	a.get("/api/v1/reservations/"+r.ID.String(), http.StatusOK, &r)
	if r.State != httpapi.AllocationStateALLOCATED || r.Instance == nil || r.Instance.GiID != 8 || r.AllocatedAt == nil {
		t.Fatalf("after bind = %+v", r)
	}

	// 반납
	a.do(http.MethodDelete, "/api/v1/reservations/"+r.ID.String(), nil, http.StatusOK, &r)
	if r.State != httpapi.AllocationStateRELEASED || r.EndedAt == nil {
		t.Fatalf("after delete = %+v", r)
	}
	a.get("/api/v1/reservations?state=RELEASED", http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].ID != r.ID {
		t.Fatalf("released = %+v", list.Items)
	}

	// 이력: 이 예약은 예약 → 확정 → 반납 순서로 남는다. 같은 정합 작업에서 시드의 다른 Pod들도
	// 관측 할당으로 잡혀 이력이 섞이므로, 이 예약 ID의 줄만 본다.
	var evs httpapi.EventList
	a.get("/api/v1/events?limit=50", http.StatusOK, &evs)
	var got []httpapi.EventType
	for i := len(evs.Items) - 1; i >= 0; i-- { // 오래된 것부터
		if ev := evs.Items[i]; ev.AllocationID != nil && *ev.AllocationID == r.ID {
			got = append(got, ev.Event)
		}
	}
	want := []httpapi.EventType{httpapi.EventTypeRESERVED, httpapi.EventTypeALLOCATED, httpapi.EventTypeRELEASED}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events for %s = %v, want %v", r.ID, got, want)
	}

	a.do(http.MethodDelete, "/api/v1/reservations/"+uuid.NewString(), nil, http.StatusNotFound, &e)
	if e.Code != "not_found" {
		t.Fatalf("not found = %+v", e)
	}
	a.get("/api/v1/reservations/not-a-uuid", http.StatusBadRequest, nil)
}

func TestReservationRejectedWhenNoCapacity(t *testing.T) {
	a := newAPI(t)
	seed(t, a.store)
	breakGPU(t, a.store) // XID 79로 fake-a100-mig의 빈 조각이 모두 비정상이 된다

	var e httpapi.Error
	a.do(http.MethodPost, "/api/v1/reservations", reservationRequest("req-x", "fake-a100-mig", "1g.5gb", "nb-1"), http.StatusConflict, &e)
	if e.Code != "no_capacity" {
		t.Fatalf("error = %+v", e)
	}
	var evs httpapi.EventList
	a.get("/api/v1/events?limit=1", http.StatusOK, &evs)
	if ev := evs.Items[0]; ev.Event != httpapi.EventTypeREJECTED || *ev.RequestID != "req-x" || ev.Workload.Name != "nb-1" {
		t.Fatalf("latest event = %+v", ev)
	}
}

func TestReservationValidation(t *testing.T) {
	a := newAPI(t)
	seed(t, a.store)

	noRequester := reservationRequest("req-1", "fake-a100-mig", "1g.5gb", "p")
	noRequester.Requester = ""
	bogus := reservationRequest("req-3", "fake-a100-mig", "1g.5gb", "p")
	bogus.Target.Kind = "JOB"

	for name, body := range map[string]any{
		"missing requester": noRequester,
		"unknown kind":      bogus,
		"not JSON":          json.RawMessage(`"just a string"`),
	} {
		t.Run(name, func(t *testing.T) {
			var e httpapi.Error
			a.do(http.MethodPost, "/api/v1/reservations", body, http.StatusBadRequest, &e)
			if e.Code != "bad_request" {
				t.Fatalf("error = %+v", e)
			}
		})
	}
	a.get("/api/v1/reservations?state=DONE", http.StatusBadRequest, nil)
}

// 완료 기준을 HTTP로도 확인한다: 빈 조각 7개(A100 1장의 1g.5gb × 7)에 동시 예약 100건.
func TestConcurrentReservationsOverHTTP(t *testing.T) {
	a := newAPI(t)
	seedMetrics(t, a.store)

	const requests = 100
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		status = map[int]int{}
	)
	for i := range requests {
		wg.Go(func() {
			b, _ := json.Marshal(reservationRequest(fmt.Sprintf("req-%03d", i), "fake-a100-mig", "1g.5gb", fmt.Sprintf("job-%03d", i)))
			resp, err := a.srv.Client().Post(a.srv.URL+"/api/v1/reservations", "application/json", bytes.NewReader(b))
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			_ = resp.Body.Close()
			mu.Lock()
			status[resp.StatusCode]++
			mu.Unlock()
		})
	}
	wg.Wait()

	if status[http.StatusCreated] != 7 || status[http.StatusConflict] != requests-7 || len(status) != 2 {
		t.Fatalf("status counts = %v, want 7×201 and 93×409", status)
	}
	var c httpapi.CapacityList
	a.get("/api/v1/capacity?node=fake-a100-mig&profile=1g.5gb", http.StatusOK, &c)
	if c.Items[0].Reserved != 7 || c.Items[0].Available != 0 {
		t.Fatalf("capacity = %+v", c.Items[0])
	}
}
