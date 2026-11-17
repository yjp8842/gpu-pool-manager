-- 장부 테이블 6개 (설계도 6장).
-- 상태 값은 ENUM 대신 text + CHECK로 둔다. 값을 추가할 때 CHECK만 바꾸면 된다.
-- updated_at은 트리거 없이 각 쿼리에서 직접 갱신한다.

-- GPU 노드. K8s Node에서 채우고, DCGM에서 먼저 보이면 이름만 넣어 둔다.
CREATE TABLE node (
    name          text        PRIMARY KEY,
    ready         boolean,    -- K8s Ready 조건. K8s에서 아직 못 읽었으면 NULL
    instance_type text,       -- node.kubernetes.io/instance-type
    last_seen_at  timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- GPU 카드 1장. 노드 정보는 이 테이블에만 둔다 (조각의 노드는 조인으로 구한다).
CREATE TABLE gpu_device (
    uuid         text        PRIMARY KEY, -- 물리 GPU UUID (GPU-...). 재부팅·재분할에도 바뀌지 않는다
    node_name    text        NOT NULL REFERENCES node (name) ON DELETE CASCADE,
    gpu_index    integer     NOT NULL,
    model        text        NOT NULL,
    memory_mb    integer,
    mig_enabled  boolean,    -- 메트릭에 MIG 모드가 없으면 NULL (MIG 상태 알 수 없음)
    total_slices integer,    -- 모델별 고정표 값. 표에 없는 모델은 NULL
    last_xid     integer,    -- 마지막 XID 오류 코드. 0이나 NULL은 오류 없음
    last_seen_at timestamptz NOT NULL,
    missed_count integer     NOT NULL DEFAULT 0, -- 연속 미관측 횟수. 3회면 지운다
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT gpu_device_gpu_index_check CHECK (gpu_index >= 0),
    CONSTRAINT gpu_device_memory_mb_check CHECK (memory_mb >= 0),
    CONSTRAINT gpu_device_total_slices_check CHECK (total_slices > 0),
    CONSTRAINT gpu_device_missed_count_check CHECK (missed_count >= 0)
);

CREATE INDEX gpu_device_node_name_idx ON gpu_device (node_name);

-- 할당 단위: MIG 조각, 또는 나누지 않은 GPU (프로파일 full, GI ID 0).
-- 할당 상태는 두지 않는다. 할당 여부는 allocation에서 읽는다.
CREATE TABLE gpu_instance (
    id                     bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    parent_uuid            text        NOT NULL REFERENCES gpu_device (uuid) ON DELETE CASCADE,
    gi_id                  integer     NOT NULL,
    profile                text        NOT NULL, -- 1g.23gb 등. 나누지 않은 GPU는 full
    memory_mb              integer,
    mig_uuid               text,       -- 현재 MIG UUID. 다시 나누면 바뀐다. 나누지 않은 GPU는 NULL
    health                 text        NOT NULL DEFAULT 'UNKNOWN',
    observed_pod_namespace text,       -- 서버1이 관측한 사용 Pod
    observed_pod_name      text,
    observed_container     text,
    last_seen_at           timestamptz NOT NULL,
    missed_count           integer     NOT NULL DEFAULT 0,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    -- 조각의 고정 키. MIG UUID는 바뀌므로 키로 쓰지 않는다
    CONSTRAINT gpu_instance_key UNIQUE (parent_uuid, gi_id, profile),
    CONSTRAINT gpu_instance_gi_id_check CHECK (gi_id >= 0),
    CONSTRAINT gpu_instance_full_gi_id_check CHECK (profile <> 'full' OR gi_id = 0),
    CONSTRAINT gpu_instance_memory_mb_check CHECK (memory_mb >= 0),
    CONSTRAINT gpu_instance_health_check CHECK (health IN ('OK', 'UNHEALTHY', 'UNKNOWN')),
    CONSTRAINT gpu_instance_observed_pod_check
        CHECK ((observed_pod_namespace IS NULL) = (observed_pod_name IS NULL)),
    CONSTRAINT gpu_instance_missed_count_check CHECK (missed_count >= 0)
);

-- GPU를 요청한 Pod. node_name은 node를 참조하지 않는다.
-- Pod와 Node를 따로 watch하므로 Pod가 먼저 들어올 수 있고, Pending Pod는 노드가 없다.
-- kind는 TM-01 할당 모델의 워크로드 종류 자리다. 지금은 POD만 쓴다.
CREATE TABLE workload (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind         text        NOT NULL DEFAULT 'POD',
    namespace    text        NOT NULL,
    name         text        NOT NULL,
    uid          text,       -- K8s UID. 같은 이름으로 다시 만든 것을 구분한다
    node_name    text,
    phase        text        NOT NULL, -- Pod phase (삭제 중이면 Terminating)
    last_seen_at timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT workload_key UNIQUE (kind, namespace, name),
    CONSTRAINT workload_kind_check CHECK (kind IN ('POD'))
);

CREATE INDEX workload_node_name_idx ON workload (node_name);

-- 예약과 할당. 할당 상태의 기준은 이 테이블 하나다.
-- 예약 경로(RESERVATION)와 예약 없이 관측된 사용(OBSERVED)이 모두 행 하나로 남는다.
-- 이 테이블을 쓰는 곳은 internal/allocation 하나다.
CREATE TABLE allocation (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    source           text        NOT NULL,
    state            text        NOT NULL,
    request_id       text,       -- 요청자가 보낸 요청 ID. 관측 할당은 NULL
    node_name        text        NOT NULL,
    profile          text        NOT NULL, -- MIG 프로파일 또는 full
    target_kind      text        NOT NULL,
    target_namespace text        NOT NULL,
    target_name      text        NOT NULL,
    requester        text,
    -- 조각이 사라지면(재분할, 노드 삭제) NULL이 된다. 정합 작업이 이런 할당을 닫는다
    instance_id      bigint      REFERENCES gpu_instance (id) ON DELETE SET NULL,
    reserved_at      timestamptz,
    expires_at       timestamptz,
    allocated_at     timestamptz,
    ended_at         timestamptz, -- 반납·만료·취소 시각
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    -- NULL끼리는 겹친 것으로 보지 않아 관측 할당은 이 제약에 걸리지 않는다
    CONSTRAINT allocation_request_id_key UNIQUE (request_id),
    CONSTRAINT allocation_source_check CHECK (source IN ('RESERVATION', 'OBSERVED')),
    CONSTRAINT allocation_state_check
        CHECK (state IN ('RESERVED', 'ALLOCATED', 'RELEASED', 'EXPIRED', 'CANCELLED')),
    CONSTRAINT allocation_target_kind_check CHECK (target_kind IN ('POD')),
    CONSTRAINT allocation_reservation_fields_check CHECK (
        source <> 'RESERVATION'
        OR (request_id IS NOT NULL AND requester IS NOT NULL
            AND reserved_at IS NOT NULL AND expires_at IS NOT NULL)
    ),
    -- 관측 할당은 요청 ID·요청자·예약·만료 시각이 비어 있고, 예약 단계를 거치지 않는다
    CONSTRAINT allocation_observed_fields_check CHECK (
        source <> 'OBSERVED'
        OR (request_id IS NULL AND requester IS NULL AND reserved_at IS NULL
            AND expires_at IS NULL AND state IN ('ALLOCATED', 'RELEASED'))
    ),
    CONSTRAINT allocation_reserved_unbound_check CHECK (state <> 'RESERVED' OR instance_id IS NULL),
    CONSTRAINT allocation_allocated_at_check
        CHECK (state IN ('RESERVED', 'EXPIRED', 'CANCELLED') OR allocated_at IS NOT NULL),
    CONSTRAINT allocation_ended_at_check
        CHECK ((state IN ('RESERVED', 'ALLOCATED')) = (ended_at IS NULL))
);

-- 중복 할당 방지. 예약 경로와 관측 경로를 같은 인덱스가 막는다.
CREATE UNIQUE INDEX allocation_instance_allocated_key ON allocation (instance_id) WHERE state = 'ALLOCATED';

-- 남은 수량 계산(같은 노드·프로파일의 RESERVED 수)과 만료 처리용.
CREATE INDEX allocation_reserved_idx ON allocation (node_name, profile) WHERE state = 'RESERVED';

-- 이력. 다른 테이블을 참조하지 않고 당시 값을 문자열로 남긴다. 조각이나 Pod가 사라져도 그대로다.
CREATE TABLE allocation_event (
    id                 bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at        timestamptz NOT NULL,
    event              text        NOT NULL,
    -- 대상 자원: 노드 + 조각(GPU UUID + GI ID + 프로파일)
    node_name          text,
    gpu_uuid           text,
    gi_id              integer,
    profile            text,
    mig_uuid           text,       -- 당시 MIG UUID
    -- 대상 워크로드와 요청자
    workload_kind      text,
    workload_namespace text,
    workload_name      text,
    requester          text,
    request_id         text,
    allocation_id      uuid,       -- allocation.id. 참조 제약은 두지 않는다
    message            text,       -- 거부 사유, 재분할 전후 구성 등
    CONSTRAINT allocation_event_event_check CHECK (event IN (
        'RESERVED', 'ALLOCATED', 'RELEASED', 'EXPIRED', 'CANCELLED', 'REJECTED', 'MIG_RECONFIGURED'
    )),
    CONSTRAINT allocation_event_workload_kind_check CHECK (workload_kind IN ('POD'))
);

CREATE INDEX allocation_event_occurred_at_idx ON allocation_event (occurred_at DESC, id DESC);
CREATE INDEX allocation_event_allocation_id_idx ON allocation_event (allocation_id);
CREATE INDEX allocation_event_instance_key_idx ON allocation_event (gpu_uuid, gi_id, profile);

-- 한 번 쓴 이력은 고치거나 지우지 않는다.
CREATE FUNCTION allocation_event_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'allocation_event is append-only (% not allowed)', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE TRIGGER allocation_event_append_only
    BEFORE UPDATE OR DELETE ON allocation_event
    FOR EACH ROW EXECUTE FUNCTION allocation_event_append_only();
