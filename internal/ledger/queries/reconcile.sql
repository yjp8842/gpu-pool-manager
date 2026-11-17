-- name: LockNodeProfile :exec
-- LockNodeProfile은 트랜잭션이 끝날 때까지 노드 + 프로파일 단위 잠금을 건다.
-- 예약은 이 잠금 안에서 남은 수량을 확인하고 행을 넣으므로, 동시에 와도 남은 수량을 넘지 않는다.
SELECT pg_advisory_xact_lock(hashtextextended(@node_name::text || '/' || @profile::text, 0));

-- name: TryLockReconcile :one
-- TryLockReconcile은 정합 작업 잠금을 시도한다. 다른 서버2가 정합 작업 중이면 false다.
SELECT pg_try_advisory_xact_lock(@lock_key::bigint) AS locked;

-- name: ExpireReservations :many
-- ExpireReservations는 만료 시각이 지난 예약을 EXPIRED로 닫는다.
UPDATE allocation SET
    state      = 'EXPIRED',
    ended_at   = @now::timestamptz,
    updated_at = now()
WHERE state = 'RESERVED' AND expires_at <= @now::timestamptz
RETURNING *;

-- name: ListReservedAllocations :many
-- ListReservedAllocations는 확정 전 예약을 먼저 만든 것부터 돌려준다.
SELECT * FROM allocation
WHERE state = 'RESERVED'
ORDER BY reserved_at, id;

-- name: ListAllocatedForReconcile :many
-- ListAllocatedForReconcile은 할당 중인 행을 묶인 조각의 지금 관측 값과 함께 돌려준다.
-- 조각이 사라졌으면 조각 컬럼이 NULL이다.
SELECT
    sqlc.embed(allocation),
    gpu_instance.parent_uuid            AS instance_gpu_uuid,
    gpu_instance.gi_id                  AS instance_gi_id,
    gpu_instance.profile                AS instance_profile,
    gpu_instance.mig_uuid               AS instance_mig_uuid,
    gpu_instance.observed_pod_namespace AS instance_pod_namespace,
    gpu_instance.observed_pod_name      AS instance_pod_name
FROM allocation
LEFT JOIN gpu_instance ON gpu_instance.id = allocation.instance_id
WHERE allocation.state = 'ALLOCATED'
ORDER BY allocation.allocated_at, allocation.id;

-- name: FindBindableInstance :one
-- FindBindableInstance는 노드 + 프로파일에서 대상 Pod가 지금 쓰고 있고 아직 할당되지 않은 조각을 찾는다.
SELECT gpu_instance.*
FROM gpu_instance
JOIN gpu_device ON gpu_device.uuid = gpu_instance.parent_uuid
WHERE gpu_device.node_name = @node_name::text
  AND gpu_instance.profile = @profile::text
  AND gpu_instance.observed_pod_namespace = @pod_namespace::text
  AND gpu_instance.observed_pod_name = @pod_name::text
  AND gpu_instance.missed_count = 0
  AND NOT EXISTS (
      SELECT 1 FROM allocation
       WHERE allocation.instance_id = gpu_instance.id AND allocation.state = 'ALLOCATED'
  )
ORDER BY gpu_device.gpu_index, gpu_instance.gi_id
LIMIT 1;

-- name: ListObservedUnallocatedInstances :many
-- ListObservedUnallocatedInstances는 이번 수집에서 Pod가 쓰는 것으로 보였지만 할당 행이 없는 조각이다.
SELECT sqlc.embed(gpu_instance), gpu_device.node_name
FROM gpu_instance
JOIN gpu_device ON gpu_device.uuid = gpu_instance.parent_uuid
WHERE gpu_instance.observed_pod_name IS NOT NULL
  AND gpu_instance.missed_count = 0
  AND NOT EXISTS (
      SELECT 1 FROM allocation
       WHERE allocation.instance_id = gpu_instance.id AND allocation.state = 'ALLOCATED'
  )
ORDER BY gpu_device.node_name, gpu_device.gpu_index, gpu_instance.gi_id;

-- name: ListReservations :many
-- ListReservations는 예약 경로로 만든 행을 최근 것부터 돌려준다.
-- state가 NULL이면 진행 중인 것(RESERVED, ALLOCATED)만 돌려준다.
SELECT * FROM allocation
WHERE source = 'RESERVATION'
  AND (
      (sqlc.narg('state')::text IS NULL AND state IN ('RESERVED', 'ALLOCATED'))
      OR state = sqlc.narg('state')
  )
ORDER BY created_at DESC, id
LIMIT sqlc.arg(row_limit);
