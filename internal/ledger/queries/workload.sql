-- name: UpsertWorkload :one
-- UpsertWorkload는 GPU를 요청한 Pod를 (종류, 네임스페이스, 이름)으로 넣거나 갱신한다.
INSERT INTO workload (
    kind, namespace, name, uid, node_name, phase, last_seen_at
) VALUES (
    @kind, @namespace, @name, @uid, @node_name, @phase, @last_seen_at
)
ON CONFLICT ON CONSTRAINT workload_key DO UPDATE SET
    uid          = EXCLUDED.uid,
    node_name    = EXCLUDED.node_name,
    phase        = EXCLUDED.phase,
    last_seen_at = EXCLUDED.last_seen_at,
    updated_at   = now()
RETURNING *;

-- name: GetWorkload :one
SELECT * FROM workload
WHERE kind = @kind AND namespace = @namespace AND name = @name;

-- name: ListWorkloads :many
-- ListWorkloads는 GPU를 요청한 Pod 목록을 돌려준다.
SELECT * FROM workload
ORDER BY kind, namespace, name;

-- name: DeleteWorkload :execrows
DELETE FROM workload
WHERE kind = @kind AND namespace = @namespace AND name = @name;
