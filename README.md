# gpu-pool-manager

GRIM-GPU 과제의 GPU Pool Manager 데모 구현이다. GPU·MIG 분할 정보를 DCGM 메트릭으로 수집해 장부에 쌓고(서버1), 요청이 오면 노드+프로파일 단위로 예약하고 이력을 남기며(서버2), 그 결과를 웹 화면으로 보여준다.

설계 근거는 "GPU Pool 데모 개발 설계서" 문서의 본문 탭과 "개발 구현 계획" 탭이다. 대상 워크로드는 Pod만 다룬다 (2026-11-17 범위 결정).

## 구성

| 경로 | 내용 |
| --- | --- |
| `cmd/collector` | 서버1 (수집 서비스) |
| `cmd/api` | 서버2 (조회·예약 서비스) |
| `internal/` | 두 서비스의 내부 패키지 (`config`, `serve`, `metrics`, `kube`, `collector`, `ledger`, `allocation`, `history`, `httpapi`). `testpg`는 DB 테스트 도우미다 |
| `api/openapi.yaml` | 서버2 API 명세. Go 서버 코드(`make gen` → `internal/httpapi/api.gen.go`)와 화면 타입의 생성 원본 |
| `migrations/` | 장부 테이블 SQL 마이그레이션 (golang-migrate 형식, 실행 파일에 포함) |
| `internal/ledger/queries/` | 장부 SQL 쿼리. `make gen`으로 `internal/ledger`의 Go 코드를 만든다 (sqlc) |
| `web/` | React + TypeScript (Vite) 화면 |
| `deploy/` | Kustomize base와 환경별 overlay (local, gcp-dev, gcp-demo) |
| `test/e2e/` | 데모 시나리오 자동 확인 |
| `test/kind/` | kind에서 3단계를 확인할 때 쓰는 예시 GPU Pod |

## 필요한 도구

- Go 1.26 이상
- Node.js 26 (npm 11)
- golangci-lint v2 (`make lint`에 필요)
- Docker (장부 테스트와 로컬 DB에 필요). sqlc와 golang-migrate는 Makefile이 고정 버전을 `go run`으로 쓰므로 따로 설치하지 않는다.

colima를 쓰면 testcontainers가 Docker를 찾도록 환경 변수 두 개를 둔다.

```bash
export DOCKER_HOST="unix://$HOME/.colima/default/docker.sock" TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
```

## 시작하기

```bash
make web-install
```

```bash
make test
```

Docker 없이 돌릴 때는 `make go-test-short`를 쓴다. 장부 DB 테스트는 건너뛴다.

```bash
make build
```

로컬 실행 (각각 다른 터미널에서):

```bash
make run-api
```

```bash
make run-collector
```

```bash
make run-web
```

서버2 없이 화면만 볼 때는 목업 API(MSW)로 띄운다. 화면에 "샘플 데이터"가 표시된다.

```bash
make run-web-mock
```

서버1은 장부 DB가 필요하다. 먼저 로컬 PostgreSQL 컨테이너를 띄운다. 서버1은 시작할 때 마이그레이션을 적용하고, `METRIC_SOURCE=fake`면 데모 구성을 흉내 낸 목업 노드 한 대(`fake-a100-mig`, A100 1장을 1g.5gb × 7로 나눔)를 장부에 채운다. `make db-psql`로 테이블을 볼 수 있다.

```bash
make db-up
```

서버를 띄우지 않고 테이블만 만들려면 `make db-migrate`를 쓴다.

### kind 클러스터에서 Node·Pod 수집 (3단계)

kind와 kubectl은 따로 설치하지 않는다. kind는 Makefile이 고정 버전을 `go run`으로 쓰고, kubectl은 kind 노드 안의 것을 쓴다. kubeconfig는 `~/.kube/config`가 아니라 레포 안 `.kube/`에 만든다.

```bash
make kind-up
```

```bash
make kind-fake-gpu
```

```bash
make run-collector-kind
```

`kind-fake-gpu`는 kind 노드를 GKE의 A100 MIG 노드처럼 꾸민다. GKE GPU 라벨(`cloud.google.com/gke-accelerator`, `cloud.google.com/gke-gpu-partition-size=1g.5gb`)과 임시 자원 `nvidia.com/gpu` 7개를 붙이고, 실제 GPU는 없다. 그 뒤 `test/kind/`의 Pod를 만들고 지우면 `workload` 테이블에 바로 반영된다.

```bash
docker exec -i gpu-pool-control-plane kubectl --kubeconfig=/etc/kubernetes/admin.conf apply -f - < test/kind/gpu-pod.yaml
```

다 쓰면 `make kind-down`으로 지운다.

서버2는 `:8080`, 서버1은 `:8081`에서 뜨고, 둘 다 `GET /healthz`로 상태를 확인할 수 있다. 화면 개발 서버는 `/api`와 `/healthz`를 서버2로 넘긴다.

## 환경 변수

| 이름 | 기본값 | 설명 |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | HTTP 수신 주소 |
| `DATABASE_URL` | (없음) | PostgreSQL 접속 주소 (`postgres://` 형식). 서버1·서버2 모두 필수 |
| `METRIC_SOURCE` | `fake` | `fake`(로컬·테스트) 또는 `dcgm`(실장비의 dcgm-exporter) |
| `K8S_WATCH` | `false` | `true`면 서버1이 K8s API에서 Node·Pod를 읽는다. `KUBECONFIG`, `~/.kube/config`, 클러스터 안 서비스 계정 순서로 연결한다 |
| `DCGM_ENDPOINTS` | (없음) | dcgm-exporter `/metrics` 주소 목록 (쉼표 구분). 항목은 `URL` 또는 `노드=URL`이고, 노드를 적지 않으면 메트릭의 `Hostname` 라벨을 노드 이름으로 쓴다. `METRIC_SOURCE=dcgm`이면 필수 |
| `SCRAPE_INTERVAL` | `15s` | 서버1의 메트릭 수집 주기 |
| `RECONCILE_INTERVAL` | `5s` | 서버2의 정합 작업 주기 |
| `RESERVATION_TTL` | `5m` | 예약 만료 시간 |

## 서버2 API

명세는 `api/openapi.yaml`이다. 목록 응답은 모두 `{ "items": [...] }`이고, 상태 값은 장부와 같은 대문자 값이다.

| 경로 | 내용 |
| --- | --- |
| `GET /api/v1/nodes` | 노드별 Ready, 인스턴스 타입, 카드 수, 조각 수, 빈 조각 수 |
| `GET /api/v1/gpus?node=` | 컨테이너용 카드와 카드별 조각 (조각마다 상태, 관측 Pod, 묶인 할당) |
| `GET /api/v1/instances?node=&profile=&state=` | 조각 목록. `state`는 `FREE`, `ALLOCATED`, `UNHEALTHY` |
| `GET /api/v1/capacity?node=&profile=` | 노드 + 프로파일별 조각 수, 빈 조각 수, 예약 수, 남은 수량, Pod가 요청할 자원 이름 |
| `GET /api/v1/workloads` | GPU를 요청한 Pod와 각자 쓰는 조각 |
| `GET /api/v1/events?limit=&before=` | 이력 (최근 것부터). 응답의 `nextBefore`를 `before`에 넣으면 다음 쪽 |
| `POST /api/v1/reservations` | 노드 + 프로파일 단위 예약. 응답의 `resourceName`(GKE에서는 `nvidia.com/gpu`)과 `nodeSelector`(조각 크기 노드 라벨)로 Pod를 만든다 |
| `GET /api/v1/reservations?state=` | 예약 목록. `state`가 없으면 진행 중인 것(`RESERVED`, `ALLOCATED`)만 |
| `GET /api/v1/reservations/{id}` | 예약 상태와 확정된 조각 |
| `DELETE /api/v1/reservations/{id}` | 확정 전이면 취소, 할당 중이면 반납 |

남은 수량(`available`)은 빈 조각 수 − 확정 전 예약 수다. 빈 조각은 건강 상태가 `OK`이고 할당되지 않은 조각이다. 로컬에서 서버1을 함께 띄우면 목업 노드 데이터로 바로 조회할 수 있다.

```bash
curl -s localhost:8080/api/v1/capacity?node=fake-a100-mig
```

응답이 명세와 맞는지는 `internal/httpapi` 테스트가 kin-openapi로 검사한다.

### 예약과 정합 작업

예약은 특정 조각이 아니라 노드 + 프로파일 단위다. Pod는 개수만 요청하고 어느 조각을 쓸지는 kubelet이 고르기 때문이다.

```bash
curl -s -X POST localhost:8080/api/v1/reservations -H 'Content-Type: application/json' \
  -d '{"requestId":"req-1","requester":"me","node":"fake-a100-mig","profile":"1g.5gb","target":{"kind":"POD","namespace":"default","name":"train-1"}}'
```

- 남은 수량 확인과 예약 생성은 노드 + 프로파일 단위 잠금을 건 한 트랜잭션에서 한다. 동시에 와도 남은 수량을 넘지 않는다. 넘는 요청은 409(`no_capacity`)이고 이력에 거부가 남는다.
- 같은 `requestId`로 같은 내용을 다시 보내면 처음 예약을 200으로 돌려주고, 다른 내용이면 409(`request_id_conflict`)다.
- 서버2는 `RECONCILE_INTERVAL`마다 정합 작업을 한다. 순서는 만료 → 반납 → 확정 → 관측 할당이다.
  - 만료: 확정 없이 `RESERVATION_TTL`이 지난 예약
  - 반납: 묶인 조각이 사라졌거나 그 조각에서 대상 Pod가 더 이상 관측되지 않는 할당
  - 확정: 대상 Pod가 예약한 노드의 같은 프로파일 조각에서 관측되면 그 조각에 묶는다
  - 관측 할당: 예약 없이 Pod가 쓰는 조각
- 예약 없는 Pod가 조각을 가져가 빈 조각이 예약 수보다 적어지면, 가장 늦게 만든 예약부터 `capacityShort: true`가 된다.

## 화면

`web/`은 React + TypeScript(Vite)다. 한 페이지에 요약 → 노드 상태 → 카드별 MIG 분할 → GPU 사용 Pod → 예약 → 할당 이력 순서로 보여주고, 30초마다 서버2에서 다시 불러온다 (설계도 8장, 목업 구성).

- API 타입(`web/src/api/schema.d.ts`)은 `api/openapi.yaml`에서 openapi-typescript로 만든다. 명세를 바꾸면 `make gen`을 돌린다.
- 목업 API는 `web/src/mocks/`에 있다. 화면 테스트와 `make run-web-mock`이 쓴다. 응답은 명세에서 만든 타입이라 명세와 모양이 다르면 컴파일되지 않는다.
- 카드별 MIG 분할에서 칸 너비는 조각의 연산 슬라이스 수(`1g` → 1칸)에 비례하고, 전체 칸 수는 카드의 `totalSlices`(A100·B200 7, RTX PRO 6000 4)를 따른다.

## 서버1 수집

서버1은 `SCRAPE_INTERVAL`마다 노드별 스냅샷을 받아 장부와 비교하고 관측 값만 쓴다. 할당 상태(`allocation`)는 바꾸지 않는다.

- 새로 보인 카드·조각은 넣고, 다시 보인 것은 갱신한다. 조각의 고정 키는 (GPU UUID, GI ID, 프로파일)이다.
- 스냅샷이 들어온 노드에서 보이지 않은 카드·조각은 미관측 횟수를 올리고, 3회 연속이면 지운다. 스냅샷을 받지 못한 노드는 세지 않는다.
- 이미 있던 GPU의 조각 구성이 바뀌면 `MIG_RECONFIGURED` 이력을 한 줄 남긴다 (예: `1g.5gb×7 → 3g.20gb×2`).
- `K8S_WATCH=true`면 Node·Pod를 informer로 받아, 바뀔 때마다(그리고 `SCRAPE_INTERVAL`마다) 장부에 반영한다.
  - 노드: GPU 노드(GPU 라벨이나 `nvidia.com/` 자원이 있음)와 DCGM에서 먼저 들어온 노드만 넣는다. K8s에서 사라진 노드는 지운다. DCGM에서만 보인 노드는 지우지 않는다.
  - 워크로드: `nvidia.com/` 자원을 요청한 Pod. K8s에서 사라지면 바로 지운다.
- `METRIC_SOURCE=dcgm`이면 `DCGM_ENDPOINTS`의 dcgm-exporter를 동시에 읽는다 (엔드포인트마다 10초 제한). 읽지 못한 엔드포인트는 경고 로그만 남기고, 그 노드는 이번 회차에 미관측으로 세지 않는다.
  - dcgm-exporter 4.8까지의 이름(`DCGM_FI_DEV_FB_USED`, `DCGM_FI_DEV_XID_ERRORS`, `Hostname`)과 4.9부터의 이름(`DCGM_FI_DEV_FB_USED_BYTES`, `DCGM_FI_DEV_XID_ERROR`, `hostname`)을 모두 읽는다.
  - MIG 모드는 수집 목록에 `DCGM_FI_DEV_MIG_MODE`를 넣어야 나온다. 없으면 조각 줄(`GPU_I_ID`)이 있는 GPU는 MIG 켜짐, 없는 GPU는 나누지 않은 GPU로 본다.
  - 조각을 쓰는 Pod는 dcgm-exporter의 Kubernetes 연결을 켜야 나온다 (`DCGM_EXPORTER_KUBERNETES=true`, GKE는 `DCGM_EXPORTER_KUBERNETES_GPU_ID_TYPE=device-name`도).
  - `internal/metrics/dcgm/testdata/`의 출력은 손으로 만든 예시다. GKE에서 실제 출력을 받으면(설계도 4장) 바꾼다.
- 조각 건강은 GPU의 마지막 XID로 정한다. 리셋이 필요한 하드웨어 계열 XID(48, 63, 64, 74, 79, 92, 95, 119, 120)만 비정상으로 본다. 설계도 10장 9번이 정해질 때까지 쓰는 임시 기준이다.

## 장부

테이블 6개는 `migrations/000001_init_ledger.up.sql`에 있다 (설계도 6장).

| 테이블 | 담는 것 | 쓰는 쪽 |
| --- | --- | --- |
| `node` | GPU 노드 | 서버1 |
| `gpu_device` | GPU 카드. 노드 정보는 여기에만 둔다 | 서버1 |
| `gpu_instance` | MIG 조각 또는 나누지 않은 GPU(`full`, GI ID 0). 할당 상태는 두지 않는다 | 서버1 (관측 값만) |
| `workload` | GPU를 요청한 Pod | 서버1 |
| `allocation` | 예약과 할당 (출처 RESERVATION / OBSERVED) | 서버2 `allocation` 패키지만 |
| `allocation_event` | 이력. 추가만 하고 고치거나 지우지 않는다 (트리거가 막음) | 서버2, 서버1(구성 변경) |

쿼리를 바꾸면 `internal/ledger/queries/`의 SQL을 고치고 `make gen`을 돌린다. 생성된 파일(`*.sql.go`, `models.go`, `db.go`)은 직접 고치지 않는다.

## 개발 단계

| 단계 | 내용 | 상태 |
| --- | --- | --- |
| 0 | 레포 뼈대, 린트, CI | 완료 |
| 1 | 장부 (마이그레이션, sqlc) | 완료 |
| 2 | 서버1 수집 (목업 메트릭) | 완료 |
| 3 | 서버1 K8s Node·Pod watch | 완료 |
| 4 | 서버2 조회 API | 완료 |
| 5 | 서버2 예약·이력 | 완료 |
| 6 | 웹 화면 | 완료 |
| 7 | 실장비 연동 (GKE a2-highgpu-1g, A100) | 진행 중: DCGM 수집기 완료, 실장비 확인 남음 |
| 8 | 클러스터 배포 | 예정 |
| 9 | 데모 리허설 (GKE a2-highgpu-1g, A100) | 예정 |

## 저장소 운영

사내 GitLab에서 개발하고, GitHub(GRIM-GPU)에는 작업 단위로 정리해 사람이 직접 커밋한다(GRIM-BG-04 방식, 유지 여부 확인 중). CI는 `.gitlab-ci.yml`에 있다.

커밋 메시지는 Conventional Commits 형식 `type(scope): 설명`으로 쓴다. `type`은 `feat`, `fix`, `docs`, `chore`, `test`, `refactor`, `build`, `ci` 중 하나이고, `scope`는 패키지 이름이다 (예: `feat(allocation): 예약 만료 처리`). 설명은 한국어로 쓰고, 공동 작성자(Co-authored-by) 줄은 적지 않는다.
