-- name: ListNodeSummaries :many
-- ListNodeSummaries는 노드마다 카드 수, 조각 수, 빈 조각 수를 함께 돌려준다 (서버2 조회 API).
-- 빈 조각: health = OK이고 그 조각을 가리키는 ALLOCATED 할당이 없음 (설계도 6장).
SELECT
    sqlc.embed(node),
    (SELECT count(*)
       FROM gpu_device
      WHERE gpu_device.node_name = node.name)::bigint AS gpu_count,
    (SELECT count(*)
       FROM gpu_instance
       JOIN gpu_device ON gpu_device.uuid = gpu_instance.parent_uuid
      WHERE gpu_device.node_name = node.name)::bigint AS instance_count,
    (SELECT count(*)
       FROM gpu_instance
       JOIN gpu_device ON gpu_device.uuid = gpu_instance.parent_uuid
      WHERE gpu_device.node_name = node.name
        AND gpu_instance.health = 'OK'
        AND NOT EXISTS (
            SELECT 1 FROM allocation
             WHERE allocation.instance_id = gpu_instance.id AND allocation.state = 'ALLOCATED'
        ))::bigint AS free_instance_count
FROM node
ORDER BY node.name;

-- name: ListInstanceViews :many
-- ListInstanceViews는 조각을 카드 정보, 지금 묶인 할당(있으면)과 함께 돌려준다.
-- node_name, profile이 NULL이면 그 조건을 걸지 않는다.
SELECT
    sqlc.embed(gpu_instance),
    gpu_device.node_name,
    gpu_device.gpu_index,
    allocation.id               AS allocation_id,
    allocation.source           AS allocation_source,
    allocation.target_kind      AS allocation_target_kind,
    allocation.target_namespace AS allocation_target_namespace,
    allocation.target_name      AS allocation_target_name,
    allocation.allocated_at     AS allocation_allocated_at
FROM gpu_instance
JOIN gpu_device ON gpu_device.uuid = gpu_instance.parent_uuid
LEFT JOIN allocation
       ON allocation.instance_id = gpu_instance.id AND allocation.state = 'ALLOCATED'
WHERE (sqlc.narg('node_name')::text IS NULL OR gpu_device.node_name = sqlc.narg('node_name'))
  AND (sqlc.narg('profile')::text IS NULL OR gpu_instance.profile = sqlc.narg('profile'))
ORDER BY gpu_device.node_name, gpu_device.gpu_index, gpu_instance.gi_id, gpu_instance.profile;

-- name: ListCapacity :many
-- ListCapacity는 노드 + 프로파일마다 조각 수, 빈 조각 수, 확정 전 예약 수를 돌려준다.
-- 남은 수량 = free_count - reserved_count. node_name, profile이 NULL이면 그 조건을 걸지 않는다.
WITH instances AS (
    SELECT
        gpu_device.node_name,
        gpu_instance.profile,
        count(*) AS total_count,
        count(*) FILTER (
            WHERE gpu_instance.health = 'OK'
              AND NOT EXISTS (
                  SELECT 1 FROM allocation
                   WHERE allocation.instance_id = gpu_instance.id AND allocation.state = 'ALLOCATED'
              )
        ) AS free_count
    FROM gpu_instance
    JOIN gpu_device ON gpu_device.uuid = gpu_instance.parent_uuid
    GROUP BY gpu_device.node_name, gpu_instance.profile
),
reservations AS (
    SELECT allocation.node_name, allocation.profile, count(*) AS reserved_count
    FROM allocation
    WHERE allocation.state = 'RESERVED'
    GROUP BY allocation.node_name, allocation.profile
)
SELECT
    instances.node_name,
    instances.profile,
    instances.total_count::bigint                        AS total_count,
    instances.free_count::bigint                         AS free_count,
    COALESCE(reservations.reserved_count, 0)::bigint     AS reserved_count
FROM instances
LEFT JOIN reservations
       ON reservations.node_name = instances.node_name AND reservations.profile = instances.profile
WHERE (sqlc.narg('node_name')::text IS NULL OR instances.node_name = sqlc.narg('node_name'))
  AND (sqlc.narg('profile')::text IS NULL OR instances.profile = sqlc.narg('profile'))
ORDER BY instances.node_name, instances.profile;
