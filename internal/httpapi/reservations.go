package httpapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/yjp8842/gpu-pool-manager/internal/allocation"
	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
)

const (
	defaultReservationLimit = 100
	maxReservationLimit     = 500
)

// ListReservations는 예약 목록을 최근 것부터 돌려준다. state가 없으면 진행 중인 것만이다.
func (s *Server) ListReservations(ctx context.Context, req ListReservationsRequestObject) (ListReservationsResponseObject, error) {
	var state *string
	if st := req.Params.State; st != nil {
		if !st.Valid() {
			return ListReservations400JSONResponse{badRequest("state: unknown value %q", *st)}, nil
		}
		state = new(string(*st))
	}
	limit := defaultReservationLimit
	if l := req.Params.Limit; l != nil {
		if *l < 1 || *l > maxReservationLimit {
			return ListReservations400JSONResponse{badRequest("limit: must be between 1 and %d", maxReservationLimit)}, nil
		}
		limit = *l
	}
	rows, err := s.store.ListReservations(ctx, ledger.ListReservationsParams{State: state, RowLimit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list reservations: %w", err)
	}
	view, err := s.reservationView(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]Reservation, 0, len(rows))
	for _, a := range rows {
		items = append(items, view.reservation(a))
	}
	return ListReservations200JSONResponse{Items: items}, nil
}

// CreateReservation은 노드 + 프로파일 단위 예약을 만든다.
func (s *Server) CreateReservation(ctx context.Context, req CreateReservationRequestObject) (CreateReservationResponseObject, error) {
	b := req.Body
	if b == nil {
		return CreateReservation400JSONResponse{badRequest("request body is required")}, nil
	}
	if !b.Target.Kind.Valid() {
		return CreateReservation400JSONResponse{badRequest("target.kind: unknown value %q (POD)", b.Target.Kind)}, nil
	}

	a, created, err := s.alloc.Reserve(ctx, allocation.ReserveRequest{
		RequestID:    b.RequestID,
		Requester:    b.Requester,
		Node:         b.Node,
		Profile:      b.Profile,
		PodNamespace: b.Target.Namespace,
		PodName:      b.Target.Name,
	})
	switch {
	case errors.Is(err, allocation.ErrInvalid):
		return CreateReservation400JSONResponse{badRequest("%s", err)}, nil
	case errors.Is(err, allocation.ErrNoCapacity):
		return CreateReservation409JSONResponse{ConflictJSONResponse{Code: "no_capacity", Message: err.Error()}}, nil
	case errors.Is(err, allocation.ErrRequestIDConflict):
		return CreateReservation409JSONResponse{ConflictJSONResponse{Code: "request_id_conflict", Message: err.Error()}}, nil
	case err != nil:
		return nil, fmt.Errorf("reserve: %w", err)
	}

	view, err := s.reservationView(ctx)
	if err != nil {
		return nil, err
	}
	if created {
		return CreateReservation201JSONResponse(view.reservation(a)), nil
	}
	return CreateReservation200JSONResponse(view.reservation(a)), nil
}

// GetReservation은 예약 하나를 돌려준다.
func (s *Server) GetReservation(ctx context.Context, req GetReservationRequestObject) (GetReservationResponseObject, error) {
	a, err := s.alloc.Get(ctx, req.ID)
	if errors.Is(err, allocation.ErrNotFound) {
		return GetReservation404JSONResponse{notFound(req.ID.String())}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get reservation: %w", err)
	}
	view, err := s.reservationView(ctx)
	if err != nil {
		return nil, err
	}
	return GetReservation200JSONResponse(view.reservation(a)), nil
}

// CancelReservation은 확정 전 예약을 취소하고, 할당 중이면 반납한다.
func (s *Server) CancelReservation(ctx context.Context, req CancelReservationRequestObject) (CancelReservationResponseObject, error) {
	a, err := s.alloc.Cancel(ctx, req.ID)
	if errors.Is(err, allocation.ErrNotFound) {
		return CancelReservation404JSONResponse{notFound(req.ID.String())}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cancel reservation: %w", err)
	}
	view, err := s.reservationView(ctx)
	if err != nil {
		return nil, err
	}
	return CancelReservation200JSONResponse(view.reservation(a)), nil
}

func notFound(id string) NotFoundJSONResponse {
	return NotFoundJSONResponse{Code: "not_found", Message: "reservation " + id + " not found"}
}

// reservationView는 예약 응답을 만드는 데 필요한 조각 정보와 용량 부족 목록이다.
type reservationView struct {
	instances map[int64]InstanceRef
	short     map[[16]byte]bool
}

func (s *Server) reservationView(ctx context.Context) (reservationView, error) {
	views, err := s.store.ListInstanceViews(ctx, ledger.ListInstanceViewsParams{})
	if err != nil {
		return reservationView{}, fmt.Errorf("list instances: %w", err)
	}
	short, err := s.alloc.ShortReservations(ctx)
	if err != nil {
		return reservationView{}, err
	}
	v := reservationView{instances: make(map[int64]InstanceRef, len(views)), short: make(map[[16]byte]bool, len(short))}
	for _, r := range views {
		in := r.GPUInstance
		v.instances[in.ID] = InstanceRef{
			ID: in.ID, GpuUUID: in.ParentUUID, GpuIndex: int(r.GPUIndex), GiID: int(in.GIID), Profile: in.Profile,
		}
	}
	for id := range short {
		v.short[id] = true
	}
	return v, nil
}

func (v reservationView) reservation(a ledger.Allocation) Reservation {
	out := Reservation{
		ID:            a.ID,
		Source:        AllocationSource(a.Source),
		State:         AllocationState(a.State),
		RequestID:     a.RequestID,
		Requester:     a.Requester,
		Node:          a.NodeName,
		Profile:       a.Profile,
		ResourceName:  podResourceName(a.Profile),
		NodeSelector:  podNodeSelector(a.Profile),
		Target:        WorkloadRef{Kind: WorkloadKind(a.TargetKind), Namespace: a.TargetNamespace, Name: a.TargetName},
		ReservedAt:    a.ReservedAt,
		ExpiresAt:     a.ExpiresAt,
		AllocatedAt:   a.AllocatedAt,
		EndedAt:       a.EndedAt,
		CapacityShort: v.short[a.ID],
	}
	if a.InstanceID != nil {
		if ref, ok := v.instances[*a.InstanceID]; ok {
			out.Instance = &ref
		}
	}
	return out
}
