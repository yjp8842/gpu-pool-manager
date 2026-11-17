// Package httpapi는 서버2의 HTTP API다 (조회는 개발 단계 4, 예약은 단계 5).
// 예약 처리 규칙은 allocation 패키지에 있고, 이 패키지는 요청·응답을 명세의 모양으로 바꾼다.
//
// api.gen.go는 api/openapi.yaml에서 oapi-codegen으로 만든 코드다(make gen). 이 파일의 Server가
// 생성된 StrictServerInterface를 구현한다. 응답 모양은 명세가 정하고, 값은 장부에서 읽는다.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/yjp8842/gpu-pool-manager/internal/allocation"
	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
)

// Server는 장부를 읽어 API에 응답하고, 예약 요청은 allocation.Service에 넘긴다.
type Server struct {
	store *ledger.Store
	alloc *allocation.Service
}

var _ StrictServerInterface = (*Server)(nil)

// NewHandler는 명세의 모든 경로를 처리하는 http.Handler를 만든다.
// 요청 값이 잘못되면 400, 장부를 읽지 못하면 500을 Error 형식으로 돌려준다.
func NewHandler(store *ledger.Store, alloc *allocation.Service, logger *slog.Logger) http.Handler {
	badRequest := func(w http.ResponseWriter, _ *http.Request, err error) {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	}
	strict := NewStrictHandlerWithOptions(&Server{store: store, alloc: alloc}, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: badRequest,
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "internal server error")
		},
	})
	return HandlerWithOptions(strict, StdHTTPServerOptions{ErrorHandlerFunc: badRequest})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Code: code, Message: message})
}

func badRequest(format string, args ...any) BadRequestJSONResponse {
	return BadRequestJSONResponse{Code: "bad_request", Message: fmt.Sprintf(format, args...)}
}

// GetHealthz는 프로세스가 살아 있으면 ok를 돌려준다.
func (s *Server) GetHealthz(context.Context, GetHealthzRequestObject) (GetHealthzResponseObject, error) {
	return GetHealthz200JSONResponse{Status: "ok"}, nil
}

// ListNodes는 노드 목록을 카드 수, 조각 수, 빈 조각 수와 함께 돌려준다.
func (s *Server) ListNodes(ctx context.Context, _ ListNodesRequestObject) (ListNodesResponseObject, error) {
	rows, err := s.store.ListNodeSummaries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	items := make([]Node, 0, len(rows))
	for _, r := range rows {
		items = append(items, Node{
			Name:              r.Node.Name,
			Ready:             r.Node.Ready,
			InstanceType:      r.Node.InstanceType,
			LastSeenAt:        r.Node.LastSeenAt,
			GpuCount:          int(r.GPUCount),
			InstanceCount:     int(r.InstanceCount),
			FreeInstanceCount: int(r.FreeInstanceCount),
		})
	}
	return ListNodes200JSONResponse{Items: items}, nil
}

// ListGPUs는 컨테이너용 GPU 카드를 카드별 조각과 함께 돌려준다.
func (s *Server) ListGPUs(ctx context.Context, req ListGPUsRequestObject) (ListGPUsResponseObject, error) {
	devices, err := s.store.ListGPUDevices(ctx, req.Params.Node)
	if err != nil {
		return nil, fmt.Errorf("list gpus: %w", err)
	}
	views, err := s.store.ListInstanceViews(ctx, ledger.ListInstanceViewsParams{NodeName: req.Params.Node})
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	byGPU := map[string][]Instance{}
	for _, v := range views {
		byGPU[v.GPUInstance.ParentUUID] = append(byGPU[v.GPUInstance.ParentUUID], instanceFromView(v))
	}
	items := make([]GPU, 0, len(devices))
	for _, d := range devices {
		items = append(items, GPU{
			UUID:        d.UUID,
			Node:        d.NodeName,
			Index:       int(d.GPUIndex),
			Model:       d.Model,
			MemoryMb:    intPtr(d.MemoryMB),
			MigEnabled:  d.MIGEnabled,
			TotalSlices: intPtr(d.TotalSlices),
			LastXid:     intPtr(d.LastXID),
			LastSeenAt:  d.LastSeenAt,
			MissedCount: int(d.MissedCount),
			Instances:   orEmpty(byGPU[d.UUID]),
		})
	}
	return ListGPUs200JSONResponse{Items: items}, nil
}

// ListInstances는 조각 목록을 돌려준다. state로 거를 수 있다.
func (s *Server) ListInstances(ctx context.Context, req ListInstancesRequestObject) (ListInstancesResponseObject, error) {
	if st := req.Params.State; st != nil && !st.Valid() {
		return ListInstances400JSONResponse{badRequest("state: unknown value %q (FREE, ALLOCATED, UNHEALTHY)", *st)}, nil
	}
	views, err := s.store.ListInstanceViews(ctx, ledger.ListInstanceViewsParams{
		NodeName: req.Params.Node, Profile: req.Params.Profile,
	})
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	items := make([]Instance, 0, len(views))
	for _, v := range views {
		in := instanceFromView(v)
		if st := req.Params.State; st != nil && in.State != *st {
			continue
		}
		items = append(items, in)
	}
	return ListInstances200JSONResponse{Items: items}, nil
}

// ListCapacity는 노드 + 프로파일별 남은 수량을 돌려준다.
func (s *Server) ListCapacity(ctx context.Context, req ListCapacityRequestObject) (ListCapacityResponseObject, error) {
	rows, err := s.store.ListCapacity(ctx, ledger.ListCapacityParams{NodeName: req.Params.Node, Profile: req.Params.Profile})
	if err != nil {
		return nil, fmt.Errorf("list capacity: %w", err)
	}
	items := make([]Capacity, 0, len(rows))
	for _, r := range rows {
		items = append(items, Capacity{
			Node:         r.NodeName,
			Profile:      r.Profile,
			ResourceName: podResourceName(r.Profile),
			NodeSelector: podNodeSelector(r.Profile),
			Total:        int(r.TotalCount),
			Free:         int(r.FreeCount),
			Reserved:     int(r.ReservedCount),
			Available:    int(max(r.FreeCount-r.ReservedCount, 0)),
		})
	}
	return ListCapacity200JSONResponse{Items: items}, nil
}

// GKE MIG 노드 풀에서 Pod가 조각을 받는 방식 (설계도 2장). 조각도 나누지 않은 GPU도 자원 이름은
// nvidia.com/gpu 하나이고, 조각 크기는 GKE가 노드 풀에 붙이는 라벨로 고른다.
const (
	gpuResourceName       = "nvidia.com/gpu"
	labelGPUPartitionSize = "cloud.google.com/gke-gpu-partition-size"
)

// podResourceName은 Pod가 요청할 K8s 자원 이름이다. GKE에서는 프로파일과 상관없이 같다.
func podResourceName(string) ResourceName {
	return gpuResourceName
}

// podNodeSelector는 Pod가 이 프로파일의 조각을 받으려면 nodeSelector에 넣을 노드 라벨이다.
// 나누지 않은 GPU는 빈 맵이다(JSON에서 null이 아니라 {}가 되도록 nil을 돌려주지 않는다).
func podNodeSelector(profile string) NodeSelector {
	if profile == "full" {
		return NodeSelector{}
	}
	return NodeSelector{labelGPUPartitionSize: profile}
}

// ListWorkloads는 GPU를 요청한 Pod를, 쓰고 있는 조각과 함께 돌려준다.
func (s *Server) ListWorkloads(ctx context.Context, _ ListWorkloadsRequestObject) (ListWorkloadsResponseObject, error) {
	ws, err := s.store.ListWorkloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workloads: %w", err)
	}
	views, err := s.store.ListInstanceViews(ctx, ledger.ListInstanceViewsParams{})
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	used := instancesByWorkload(views)

	items := make([]Workload, 0, len(ws))
	for _, w := range ws {
		items = append(items, Workload{
			Kind:       WorkloadKind(w.Kind),
			Namespace:  w.Namespace,
			Name:       w.Name,
			UID:        w.UID,
			Node:       w.NodeName,
			Phase:      w.Phase,
			LastSeenAt: w.LastSeenAt,
			Instances:  orEmpty(used[workloadKey{w.Kind, w.Namespace, w.Name}]),
		})
	}
	return ListWorkloads200JSONResponse{Items: items}, nil
}

type workloadKey struct{ kind, namespace, name string }

// instancesByWorkload는 워크로드마다 쓰고 있는 조각을 모은다. DCGM이 관측한 Pod와 할당 대상을 함께 본다.
func instancesByWorkload(views []ledger.ListInstanceViewsRow) map[workloadKey][]InstanceRef {
	out := map[workloadKey][]InstanceRef{}
	add := func(k workloadKey, v ledger.ListInstanceViewsRow) {
		for _, r := range out[k] {
			if r.ID == v.GPUInstance.ID {
				return
			}
		}
		out[k] = append(out[k], InstanceRef{
			ID: v.GPUInstance.ID, GpuUUID: v.GPUInstance.ParentUUID, GpuIndex: int(v.GPUIndex),
			GiID: int(v.GPUInstance.GIID), Profile: v.GPUInstance.Profile,
		})
	}
	for _, v := range views {
		in := v.GPUInstance
		if in.ObservedPodNamespace != nil && in.ObservedPodName != nil {
			add(workloadKey{ledger.KindPod, *in.ObservedPodNamespace, *in.ObservedPodName}, v)
		}
		if v.AllocationTargetKind != nil && v.AllocationTargetNamespace != nil && v.AllocationTargetName != nil {
			add(workloadKey{*v.AllocationTargetKind, *v.AllocationTargetNamespace, *v.AllocationTargetName}, v)
		}
	}
	return out
}

const (
	defaultEventLimit = 100
	maxEventLimit     = 500
)

// ListEvents는 이력을 최근 것부터 돌려준다. before로 다음 쪽을 받는다.
func (s *Server) ListEvents(ctx context.Context, req ListEventsRequestObject) (ListEventsResponseObject, error) {
	limit := defaultEventLimit
	if l := req.Params.Limit; l != nil {
		if *l < 1 || *l > maxEventLimit {
			return ListEvents400JSONResponse{badRequest("limit: must be between 1 and %d", maxEventLimit)}, nil
		}
		limit = *l
	}
	// 한 개 더 읽어 다음 쪽이 있는지 안다.
	rows, err := s.store.ListEvents(ctx, ledger.ListEventsParams{BeforeID: req.Params.Before, RowLimit: int32(limit + 1)})
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	resp := ListEvents200JSONResponse{Items: make([]Event, 0, min(len(rows), limit))}
	if len(rows) > limit {
		rows = rows[:limit]
		resp.NextBefore = &rows[limit-1].ID
	}
	for _, e := range rows {
		resp.Items = append(resp.Items, eventFromRow(e))
	}
	return resp, nil
}

func instanceFromView(v ledger.ListInstanceViewsRow) Instance {
	in := v.GPUInstance
	out := Instance{
		ID:          in.ID,
		Node:        v.NodeName,
		GpuUUID:     in.ParentUUID,
		GpuIndex:    int(v.GPUIndex),
		GiID:        int(in.GIID),
		Profile:     in.Profile,
		MemoryMb:    intPtr(in.MemoryMB),
		MigUUID:     in.MIGUUID,
		Health:      InstanceHealth(in.Health),
		LastSeenAt:  in.LastSeenAt,
		MissedCount: int(in.MissedCount),
	}
	if in.ObservedPodNamespace != nil && in.ObservedPodName != nil {
		out.ObservedPod = &PodRef{Namespace: *in.ObservedPodNamespace, Name: *in.ObservedPodName, Container: in.ObservedContainer}
	}
	if v.AllocationID != nil {
		out.Allocation = &InstanceAllocation{
			ID:     *v.AllocationID,
			Source: AllocationSource(deref(v.AllocationSource)),
			Target: WorkloadRef{
				Kind:      WorkloadKind(deref(v.AllocationTargetKind)),
				Namespace: deref(v.AllocationTargetNamespace),
				Name:      deref(v.AllocationTargetName),
			},
		}
		if v.AllocationAllocatedAt != nil {
			out.Allocation.AllocatedAt = *v.AllocationAllocatedAt
		}
	}
	switch {
	case out.Allocation != nil:
		out.State = InstanceStateALLOCATED
	case in.Health != ledger.HealthOK:
		out.State = InstanceStateUNHEALTHY
	default:
		out.State = InstanceStateFREE
	}
	return out
}

func eventFromRow(e ledger.AllocationEvent) Event {
	out := Event{
		ID:           e.ID,
		OccurredAt:   e.OccurredAt,
		Event:        EventType(e.Event),
		Node:         e.NodeName,
		GpuUUID:      e.GPUUUID,
		GiID:         intPtr(e.GIID),
		Profile:      e.Profile,
		MigUUID:      e.MIGUUID,
		Requester:    e.Requester,
		RequestID:    e.RequestID,
		AllocationID: e.AllocationID,
		Message:      e.Message,
	}
	if e.WorkloadKind != nil && e.WorkloadNamespace != nil && e.WorkloadName != nil {
		out.Workload = &WorkloadRef{Kind: WorkloadKind(*e.WorkloadKind), Namespace: *e.WorkloadNamespace, Name: *e.WorkloadName}
	}
	return out
}

// orEmpty는 nil 슬라이스를 빈 슬라이스로 바꾼다. 명세의 배열은 null이 아니라 []여야 한다.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func intPtr(v *int32) *int {
	if v == nil {
		return nil
	}
	return new(int(*v))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
