-- name: AppendEvent :one
-- AppendEvent는 이력 한 줄을 추가한다. 이력은 고치거나 지우지 않는다 (트리거가 막는다).
INSERT INTO allocation_event (
    occurred_at, event, node_name, gpu_uuid, gi_id, profile, mig_uuid,
    workload_kind, workload_namespace, workload_name,
    requester, request_id, allocation_id, message
) VALUES (
    @occurred_at, @event, @node_name, @gpu_uuid, @gi_id, @profile, @mig_uuid,
    @workload_kind, @workload_namespace, @workload_name,
    @requester, @request_id, @allocation_id, @message
)
RETURNING *;

-- name: ListEvents :many
-- ListEvents는 최근 이력부터 row_limit개를 돌려준다. 최근은 추가된 순서(id)로 본다.
-- before_id가 있으면 그보다 앞선 이력만 돌려준다 (다음 쪽).
SELECT * FROM allocation_event
WHERE sqlc.narg('before_id')::bigint IS NULL OR id < sqlc.narg('before_id')
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListEventsByAllocation :many
SELECT * FROM allocation_event
WHERE allocation_id = @allocation_id::uuid
ORDER BY occurred_at, id;

-- name: ListEventsByInstanceKey :many
-- ListEventsByInstanceKey는 조각의 고정 키로 이력을 찾는다. MIG를 다시 나눠도 이전 조각의 이력이 그대로 나온다.
SELECT * FROM allocation_event
WHERE gpu_uuid = @gpu_uuid::text AND gi_id = @gi_id::integer AND profile = @profile::text
ORDER BY occurred_at, id;
