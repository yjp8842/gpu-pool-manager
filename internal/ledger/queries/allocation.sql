-- name: CreateReservation :one
-- CreateReservation은 노드 + 프로파일 단위 예약을 만든다. 실제 조각은 대상 Pod가 관측될 때
-- BindReservation으로 묶는다. 남은 수량 확인은 호출한 쪽이 같은 트랜잭션에서 한다.
INSERT INTO allocation (
    source, state, request_id, node_name, profile,
    target_kind, target_namespace, target_name, requester, reserved_at, expires_at
) VALUES (
    'RESERVATION', 'RESERVED', @request_id::text, @node_name, @profile,
    @target_kind, @target_namespace, @target_name, @requester::text,
    @reserved_at::timestamptz, @expires_at::timestamptz
)
RETURNING *;

-- name: CreateObservedAllocation :one
-- CreateObservedAllocation은 예약 없이 관측된 사용을 바로 할당으로 남긴다 (관측 할당).
-- 같은 조각에 이미 ALLOCATED 행이 있으면 유일 인덱스 allocation_instance_allocated_key에 걸린다.
INSERT INTO allocation (
    source, state, node_name, profile,
    target_kind, target_namespace, target_name, instance_id, allocated_at
) VALUES (
    'OBSERVED', 'ALLOCATED', @node_name, @profile,
    @target_kind, @target_namespace, @target_name, sqlc.narg('instance_id'), @allocated_at::timestamptz
)
RETURNING *;

-- name: GetAllocation :one
SELECT * FROM allocation
WHERE id = @id;

-- name: GetAllocationByRequestID :one
SELECT * FROM allocation
WHERE request_id = @request_id::text;

-- name: BindReservation :one
-- BindReservation은 예약을 실제 조각에 묶어 확정한다. RESERVED가 아니면 바뀌지 않고 pgx.ErrNoRows를 돌려준다.
UPDATE allocation SET
    state        = 'ALLOCATED',
    instance_id  = sqlc.narg('instance_id'),
    allocated_at = @allocated_at::timestamptz,
    updated_at   = now()
WHERE id = @id AND state = 'RESERVED'
RETURNING *;

-- name: CloseAllocation :one
-- CloseAllocation은 열린 예약·할당을 닫는다 (RELEASED, EXPIRED, CANCELLED).
-- 이미 닫혔으면 바뀌지 않고 pgx.ErrNoRows를 돌려준다. 상태 조합은 CHECK 제약이 확인한다.
UPDATE allocation SET
    state      = @state,
    ended_at   = @ended_at::timestamptz,
    updated_at = now()
WHERE id = @id AND state IN ('RESERVED', 'ALLOCATED')
RETURNING *;

-- name: GetCapacity :one
-- GetCapacity는 노드 + 프로파일의 남은 수량 재료를 돌려준다. 남은 수량 = free_count - reserved_count.
-- 빈 조각: health = OK이고 그 조각을 가리키는 ALLOCATED 행이 없음.
SELECT
    (SELECT count(*)
       FROM gpu_instance
       JOIN gpu_device ON gpu_device.uuid = gpu_instance.parent_uuid
      WHERE gpu_device.node_name = @node_name::text
        AND gpu_instance.profile = @profile::text
        AND gpu_instance.health = 'OK'
        AND NOT EXISTS (
            SELECT 1 FROM allocation
             WHERE allocation.instance_id = gpu_instance.id AND allocation.state = 'ALLOCATED'
        ))::bigint AS free_count,
    (SELECT count(*)
       FROM allocation
      WHERE allocation.node_name = @node_name::text
        AND allocation.profile = @profile::text
        AND allocation.state = 'RESERVED')::bigint AS reserved_count;
