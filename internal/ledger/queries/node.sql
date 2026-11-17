-- name: UpsertNode :one
-- UpsertNode는 K8s Node에서 읽은 값으로 노드를 넣거나 갱신한다.
INSERT INTO node (name, ready, instance_type, last_seen_at)
VALUES (@name, @ready, @instance_type, @last_seen_at)
ON CONFLICT (name) DO UPDATE SET
    ready         = EXCLUDED.ready,
    instance_type = EXCLUDED.instance_type,
    last_seen_at  = EXCLUDED.last_seen_at,
    updated_at    = now()
RETURNING *;

-- name: EnsureNode :exec
-- EnsureNode는 DCGM에서 먼저 보인 노드를 이름만으로 넣어 둔다. 이미 있으면 그대로 둔다.
INSERT INTO node (name, last_seen_at)
VALUES (@name, @last_seen_at)
ON CONFLICT (name) DO NOTHING;

-- name: GetNode :one
SELECT * FROM node
WHERE name = @name;

-- name: ListNodes :many
SELECT * FROM node
ORDER BY name;

-- name: DeleteNode :execrows
-- DeleteNode는 노드와 그 노드의 GPU 카드·조각을 함께 지운다.
DELETE FROM node
WHERE name = @name;
