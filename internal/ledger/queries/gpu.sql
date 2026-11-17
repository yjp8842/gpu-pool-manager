-- name: UpsertGPUDevice :one
-- UpsertGPUDevice는 관측한 GPU 카드를 넣거나 갱신하고 미관측 횟수를 0으로 되돌린다.
INSERT INTO gpu_device (
    uuid, node_name, gpu_index, model, memory_mb, mig_enabled, total_slices, last_xid, last_seen_at
) VALUES (
    @uuid, @node_name, @gpu_index, @model, @memory_mb, @mig_enabled, @total_slices, @last_xid, @last_seen_at
)
ON CONFLICT (uuid) DO UPDATE SET
    node_name    = EXCLUDED.node_name,
    gpu_index    = EXCLUDED.gpu_index,
    model        = EXCLUDED.model,
    memory_mb    = EXCLUDED.memory_mb,
    mig_enabled  = EXCLUDED.mig_enabled,
    total_slices = EXCLUDED.total_slices,
    last_xid     = EXCLUDED.last_xid,
    last_seen_at = EXCLUDED.last_seen_at,
    missed_count = 0,
    updated_at   = now()
RETURNING *;

-- name: GetGPUDevice :one
SELECT * FROM gpu_device
WHERE uuid = @uuid;

-- name: ListGPUDevices :many
-- ListGPUDevices는 GPU 카드 목록을 돌려준다. node_name이 NULL이면 전체다.
SELECT * FROM gpu_device
WHERE sqlc.narg('node_name')::text IS NULL OR node_name = sqlc.narg('node_name')
ORDER BY node_name, gpu_index, uuid;

-- name: DeleteGPUDevice :execrows
-- DeleteGPUDevice는 카드와 그 카드의 조각을 함께 지운다.
DELETE FROM gpu_device
WHERE uuid = @uuid;

-- name: UpsertGPUInstance :one
-- UpsertGPUInstance는 고정 키(부모 GPU UUID, GI ID, 프로파일)로 조각을 넣거나 갱신한다.
-- MIG UUID와 관측 값은 매번 현재 값으로 덮어쓴다.
INSERT INTO gpu_instance (
    parent_uuid, gi_id, profile, memory_mb, mig_uuid, health,
    observed_pod_namespace, observed_pod_name, observed_container, last_seen_at
) VALUES (
    @parent_uuid, @gi_id, @profile, @memory_mb, @mig_uuid, @health,
    @observed_pod_namespace, @observed_pod_name, @observed_container, @last_seen_at
)
ON CONFLICT ON CONSTRAINT gpu_instance_key DO UPDATE SET
    memory_mb              = EXCLUDED.memory_mb,
    mig_uuid               = EXCLUDED.mig_uuid,
    health                 = EXCLUDED.health,
    observed_pod_namespace = EXCLUDED.observed_pod_namespace,
    observed_pod_name      = EXCLUDED.observed_pod_name,
    observed_container     = EXCLUDED.observed_container,
    last_seen_at           = EXCLUDED.last_seen_at,
    missed_count           = 0,
    updated_at             = now()
RETURNING *;

-- name: GetGPUInstance :one
SELECT * FROM gpu_instance
WHERE id = @id;

-- name: ListGPUInstances :many
-- ListGPUInstances는 조각 목록을 카드의 노드·번호와 함께 돌려준다. 노드 정보는 gpu_device에서 조인한다.
-- node_name, profile이 NULL이면 그 조건을 걸지 않는다.
SELECT sqlc.embed(gpu_instance), gpu_device.node_name, gpu_device.gpu_index
FROM gpu_instance
JOIN gpu_device ON gpu_device.uuid = gpu_instance.parent_uuid
WHERE (sqlc.narg('node_name')::text IS NULL OR gpu_device.node_name = sqlc.narg('node_name'))
  AND (sqlc.narg('profile')::text IS NULL OR gpu_instance.profile = sqlc.narg('profile'))
ORDER BY gpu_device.node_name, gpu_device.gpu_index, gpu_instance.gi_id, gpu_instance.profile;

-- name: DeleteGPUInstance :execrows
-- DeleteGPUInstance는 조각을 지운다. 이 조각을 가리키던 할당의 instance_id는 NULL이 된다.
DELETE FROM gpu_instance
WHERE id = @id;

-- ---- 미관측 처리 (서버1). 스냅샷이 들어온 노드에만 쓴다. ----

-- name: MarkMissingGPUDevices :execrows
-- MarkMissingGPUDevices는 node_name 노드의 카드 중 seen_uuids에 없는 것의 미관측 횟수를 1 올린다.
-- seen_uuids는 빈 배열이어도 되지만 NULL이면 안 된다.
UPDATE gpu_device SET
    missed_count = missed_count + 1,
    updated_at   = now()
WHERE node_name = @node_name
  AND NOT (uuid = ANY (@seen_uuids::text[]));

-- name: DeleteMissingGPUDevices :many
-- DeleteMissingGPUDevices는 node_name 노드에서 miss_limit번 연속 보이지 않은 카드를 지운다. 그 카드의 조각도 함께 지워진다.
DELETE FROM gpu_device
WHERE node_name = @node_name
  AND missed_count >= @miss_limit::integer
RETURNING uuid;

-- name: MarkMissingGPUInstances :execrows
-- MarkMissingGPUInstances는 node_name 노드 카드의 조각 중 seen_ids에 없는 것의 미관측 횟수를 1 올린다.
UPDATE gpu_instance SET
    missed_count = missed_count + 1,
    updated_at   = now()
WHERE parent_uuid IN (SELECT gpu_device.uuid FROM gpu_device WHERE gpu_device.node_name = @node_name)
  AND NOT (id = ANY (@seen_ids::bigint[]));

-- name: DeleteMissingGPUInstances :many
-- DeleteMissingGPUInstances는 node_name 노드 카드의 조각 중 miss_limit번 연속 보이지 않은 것을 지운다.
-- 이 조각을 가리키던 할당의 instance_id는 NULL이 된다.
DELETE FROM gpu_instance
WHERE parent_uuid IN (SELECT gpu_device.uuid FROM gpu_device WHERE gpu_device.node_name = @node_name)
  AND missed_count >= @miss_limit::integer
RETURNING id, parent_uuid, gi_id, profile;
