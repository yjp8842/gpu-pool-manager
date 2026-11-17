package ledger

// 컬럼 값. migrations/의 CHECK 제약과 같은 값이다.
const (
	// gpu_instance.health
	HealthOK        = "OK"
	HealthUnhealthy = "UNHEALTHY"
	HealthUnknown   = "UNKNOWN"

	// workload.kind, allocation.target_kind, allocation_event.workload_kind. 지금은 Pod만 쓴다.
	KindPod = "POD"

	// allocation.source
	SourceReservation = "RESERVATION"
	SourceObserved    = "OBSERVED"

	// allocation.state
	StateReserved  = "RESERVED"
	StateAllocated = "ALLOCATED"
	StateReleased  = "RELEASED"
	StateExpired   = "EXPIRED"
	StateCancelled = "CANCELLED"

	// allocation_event.event
	EventReserved        = "RESERVED"
	EventAllocated       = "ALLOCATED"
	EventReleased        = "RELEASED"
	EventExpired         = "EXPIRED"
	EventCancelled       = "CANCELLED"
	EventRejected        = "REJECTED"
	EventMIGReconfigured = "MIG_RECONFIGURED"
)

// 오류를 구분할 때 쓰는 제약 이름.
const (
	// ConstraintRequestID는 같은 요청 ID로 예약을 두 번 만들 때 걸린다.
	ConstraintRequestID = "allocation_request_id_key"
	// ConstraintInstanceAllocated는 한 조각에 ALLOCATED 행이 둘 생길 때 걸린다 (중복 할당).
	ConstraintInstanceAllocated = "allocation_instance_allocated_key"
)
