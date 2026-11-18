// Package allocation은 남은 수량 계산, 예약·취소, 정합 작업(확정·반납·만료·관측 할당)을 맡는다.
//
// allocation 테이블을 쓰는 곳은 이 패키지 하나다. 예약과 관측 할당은 출처(source)로 구분하고,
// 상태가 바뀔 때마다 같은 트랜잭션에서 이력을 남긴다.
//
// 예약은 노드 + 프로파일 단위다. 어느 조각을 쓸지는 kubelet이 고르므로, 실제 조각은 대상 Pod가
// 그 노드의 같은 프로파일 조각에서 관측될 때 정합 작업(Reconcile)이 묶는다.
package allocation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/yjp8842/gpu-pool-manager/internal/history"
	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
)

var (
	// ErrInvalid는 예약 요청 값이 비었거나 맞지 않을 때의 오류다.
	ErrInvalid = errors.New("invalid reservation request")
	// ErrNoCapacity는 노드 + 프로파일의 남은 수량이 0이라 예약을 거부했을 때의 오류다. 거부도 이력에 남는다.
	ErrNoCapacity = errors.New("no capacity left")
	// ErrRequestIDConflict는 같은 요청 ID로 다른 내용의 예약을 다시 요청했을 때의 오류다.
	ErrRequestIDConflict = errors.New("request id already used for a different reservation")
	// ErrNotFound는 예약(할당) ID가 없을 때의 오류다.
	ErrNotFound = ledger.ErrNotFound
)

// Config는 Service 설정이다.
type Config struct {
	TTL    time.Duration    // 예약 만료 시간 (RESERVATION_TTL)
	Now    func() time.Time // 지금 시각. 비우면 time.Now
	Logger *slog.Logger
}

// Service는 예약과 정합 작업을 처리한다.
type Service struct {
	store  *ledger.Store
	ttl    time.Duration
	now    func() time.Time
	logger *slog.Logger
}

// New는 Service를 만든다.
func New(store *ledger.Store, cfg Config) *Service {
	s := &Service{store: store, ttl: cfg.TTL, now: cfg.Now, logger: cfg.Logger}
	if s.now == nil {
		s.now = time.Now
	}
	if s.logger == nil {
		s.logger = slog.New(slog.DiscardHandler)
	}
	return s
}

// ReserveRequest는 예약 요청이다. 대상은 요청자가 이 예약으로 만들 Pod다.
type ReserveRequest struct {
	RequestID    string // 요청자가 정한 ID. 같은 ID로 다시 보내면 처음 예약을 그대로 돌려준다
	Requester    string
	Node         string
	Profile      string // MIG 프로파일(1g.5gb) 또는 full
	PodNamespace string
	PodName      string
}

func (r ReserveRequest) validate() error {
	for _, f := range []struct{ name, v string }{
		{"requestId", r.RequestID}, {"requester", r.Requester}, {"node", r.Node},
		{"profile", r.Profile}, {"target.namespace", r.PodNamespace}, {"target.name", r.PodName},
	} {
		if f.v == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalid, f.name)
		}
	}
	return nil
}

func (r ReserveRequest) sameAs(a ledger.Allocation) bool {
	return a.Source == ledger.SourceReservation && a.NodeName == r.Node && a.Profile == r.Profile &&
		a.TargetKind == ledger.KindPod && a.TargetNamespace == r.PodNamespace && a.TargetName == r.PodName &&
		a.Requester != nil && *a.Requester == r.Requester
}

// Reserve는 노드 + 프로파일 단위 예약을 만든다. 새로 만들었으면 created가 true다.
// 같은 요청 ID로 같은 내용을 다시 보내면 처음 예약을 돌려준다(created false).
// 남은 수량이 0이면 거부 이력을 남기고 ErrNoCapacity를 돌려준다.
func (s *Service) Reserve(ctx context.Context, req ReserveRequest) (a ledger.Allocation, created bool, err error) {
	if err := req.validate(); err != nil {
		return ledger.Allocation{}, false, err
	}
	if a, err := s.existing(ctx, req); err == nil || !errors.Is(err, ErrNotFound) {
		return a, false, err
	}

	now := s.now()
	var rejected string
	err = s.store.InTx(ctx, func(q *ledger.Queries) error {
		rejected = ""
		if err := q.LockNodeProfile(ctx, ledger.LockNodeProfileParams{NodeName: req.Node, Profile: req.Profile}); err != nil {
			return fmt.Errorf("lock: %w", err)
		}
		c, err := q.GetCapacity(ctx, ledger.GetCapacityParams{NodeName: req.Node, Profile: req.Profile})
		if err != nil {
			return fmt.Errorf("capacity: %w", err)
		}
		if c.FreeCount-c.ReservedCount < 1 {
			rejected = fmt.Sprintf("남은 수량 0 (빈 조각 %d, 확정 전 예약 %d)", c.FreeCount, c.ReservedCount)
			return history.Rejected(ctx, q, now, history.Request{
				Node: req.Node, Profile: req.Profile, Kind: ledger.KindPod,
				Namespace: req.PodNamespace, Name: req.PodName, Requester: req.Requester, RequestID: req.RequestID,
			}, rejected)
		}
		a, err = q.CreateReservation(ctx, ledger.CreateReservationParams{
			RequestID:       req.RequestID,
			NodeName:        req.Node,
			Profile:         req.Profile,
			TargetKind:      ledger.KindPod,
			TargetNamespace: req.PodNamespace,
			TargetName:      req.PodName,
			Requester:       req.Requester,
			ReservedAt:      now,
			ExpiresAt:       now.Add(s.ttl),
		})
		if err != nil {
			return fmt.Errorf("create reservation: %w", err)
		}
		return history.Allocation(ctx, q, now, ledger.EventReserved, a, nil, "")
	})
	switch {
	case ledger.IsUniqueViolation(err, ledger.ConstraintRequestID):
		// 같은 요청 ID가 동시에 들어와 다른 쪽이 먼저 만들었다.
		a, err := s.existing(ctx, req)
		return a, false, err
	case err != nil:
		return ledger.Allocation{}, false, err
	case rejected != "":
		return ledger.Allocation{}, false, fmt.Errorf("%w: %s", ErrNoCapacity, rejected)
	}
	return a, true, nil
}

// existing은 같은 요청 ID의 예약을 찾는다. 내용이 다르면 ErrRequestIDConflict다.
func (s *Service) existing(ctx context.Context, req ReserveRequest) (ledger.Allocation, error) {
	a, err := s.store.GetAllocationByRequestID(ctx, req.RequestID)
	if err != nil {
		return ledger.Allocation{}, err
	}
	if !req.sameAs(a) {
		return ledger.Allocation{}, ErrRequestIDConflict
	}
	return a, nil
}

// Get은 예약(할당) 하나를 돌려준다.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (ledger.Allocation, error) {
	return s.store.GetAllocation(ctx, id)
}

// Cancel은 확정 전 예약을 취소하고(CANCELLED), 할당 중이면 반납한다(RELEASED).
// 이미 닫힌 행이면 바꾸지 않고 지금 상태를 돌려준다.
// 반납한 조각을 Pod가 계속 쓰고 있으면 다음 정합 작업이 관측 할당으로 다시 잡는다.
func (s *Service) Cancel(ctx context.Context, id uuid.UUID) (ledger.Allocation, error) {
	now := s.now()
	var out ledger.Allocation
	err := s.store.InTx(ctx, func(q *ledger.Queries) error {
		a, err := q.GetAllocation(ctx, id)
		if err != nil {
			return err
		}
		event, state := ledger.EventCancelled, ledger.StateCancelled
		switch a.State {
		case ledger.StateReserved:
		case ledger.StateAllocated:
			event, state = ledger.EventReleased, ledger.StateReleased
		default:
			out = a
			return nil
		}
		in, err := instanceOf(ctx, q, a.InstanceID)
		if err != nil {
			return err
		}
		closed, err := q.CloseAllocation(ctx, ledger.CloseAllocationParams{ID: id, State: state, EndedAt: now})
		if errors.Is(err, ledger.ErrNotFound) {
			// 정합 작업이 먼저 닫았다.
			out, err = q.GetAllocation(ctx, id)
			return err
		}
		if err != nil {
			return fmt.Errorf("close: %w", err)
		}
		out = closed
		return history.Allocation(ctx, q, now, event, closed, in, "반납 호출")
	})
	return out, err
}

// instanceOf는 할당이 가리키는 조각의 이력용 키를 읽는다. 조각이 없으면 nil이다.
func instanceOf(ctx context.Context, q *ledger.Queries, id *int64) (*history.Instance, error) {
	if id == nil {
		return nil, nil
	}
	in, err := q.GetGPUInstance(ctx, *id)
	if errors.Is(err, ledger.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("instance %d: %w", *id, err)
	}
	return &history.Instance{GPUUUID: in.ParentUUID, GIID: in.GIID, Profile: in.Profile, MIGUUID: in.MIGUUID}, nil
}

// ShortReservations는 "용량 부족"인 확정 전 예약 ID를 돌려준다. 관측 할당이 늘어 빈 조각이 예약 수보다
// 적어지면, 노드 + 프로파일마다 가장 늦게 만든 예약부터 차례가 오지 않는다.
func (s *Service) ShortReservations(ctx context.Context) (map[uuid.UUID]bool, error) {
	reserved, err := s.store.ListReservedAllocations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list reserved: %w", err)
	}
	caps, err := s.store.ListCapacity(ctx, ledger.ListCapacityParams{})
	if err != nil {
		return nil, fmt.Errorf("list capacity: %w", err)
	}
	type key struct{ node, profile string }
	free := map[key]int64{}
	for _, c := range caps {
		free[key{c.NodeName, c.Profile}] = c.FreeCount
	}
	seen := map[key]int64{}
	short := map[uuid.UUID]bool{}
	for _, a := range reserved { // 먼저 만든 것부터
		k := key{a.NodeName, a.Profile}
		if seen[k] >= free[k] {
			short[a.ID] = true
		}
		seen[k]++
	}
	return short, nil
}
