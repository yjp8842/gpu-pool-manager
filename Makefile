GO            ?= go
GOLANGCI_LINT ?= golangci-lint
NPM           ?= npm
BIN           := bin
# ./... 는 web/node_modules 안의 Go 파일까지 잡으므로 Go 패키지 경로를 명시한다.
GO_PKGS       := ./cmd/... ./internal/... ./migrations/...

# 코드 생성·마이그레이션 도구는 설치하지 않고 고정한 버전을 go run으로 쓴다.
SQLC_VERSION    := v1.31.1
OAPI_VERSION    := v2.8.2
MIGRATE_VERSION := v4.20.1
SQLC            := $(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
OAPI            := $(GO) run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_VERSION)
OAPI_OUT        := internal/httpapi/api.gen.go
MIGRATE         := $(GO) run -tags pgx5 github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)

# 로컬 클러스터 (make kind-up). 개인 ~/.kube/config를 건드리지 않도록 kubeconfig를 레포 안에 따로 둔다.
KIND_VERSION     := v0.33.0
KIND             := $(GO) run sigs.k8s.io/kind@$(KIND_VERSION)
KIND_CLUSTER     := gpu-pool
KIND_KUBECONFIG  := $(CURDIR)/.kube/kind-$(KIND_CLUSTER).yaml
KIND_NODE        := $(KIND_CLUSTER)-control-plane
# kubectl을 따로 설치하지 않아도 되게 kind 노드 안의 kubectl을 쓴다.
KUBECTL          ?= docker exec -i $(KIND_NODE) kubectl --kubeconfig=/etc/kubernetes/admin.conf

# 로컬 PostgreSQL (make db-up)
DB_CONTAINER       := gpu-pool-postgres
DB_IMAGE           := postgres:18-alpine
LOCAL_DATABASE_URL := postgres://gpupool:gpupool@localhost:5432/gpupool?sslmode=disable

.PHONY: all build test lint fmt gen gen-check \
	go-build go-test go-test-short go-vet go-lint \
	web-install web-build web-test web-lint \
	db-up db-down db-migrate db-psql \
	kind-up kind-fake-gpu kind-down \
	run-collector run-collector-kind run-api run-web run-web-mock clean

all: lint test build

build: go-build web-build
test: go-test web-test
lint: go-lint web-lint

go-build:
	$(GO) build -o $(BIN)/collector ./cmd/collector
	$(GO) build -o $(BIN)/api ./cmd/api

# 장부 테스트는 testcontainers로 PostgreSQL 컨테이너를 띄운다 (Docker 필요).
go-test:
	$(GO) test $(GO_PKGS)

# Docker 없이 돌릴 수 있는 테스트만 (DB 테스트는 건너뛴다).
go-test-short:
	$(GO) test -short $(GO_PKGS)

go-vet:
	$(GO) vet $(GO_PKGS)

go-lint: go-vet
	$(GOLANGCI_LINT) run $(GO_PKGS)

# 코드 생성: 장부 쿼리(sqlc)와 서버2 API(oapi-codegen).
#   internal/ledger/queries/*.sql + migrations/*.sql → internal/ledger/*.sql.go
#   api/openapi.yaml → internal/httpapi/api.gen.go, web/src/api/schema.d.ts
gen:
	$(SQLC) generate
	$(OAPI) -config api/oapi-codegen.yaml -o $(OAPI_OUT) api/openapi.yaml
	cd web && $(NPM) run gen:api

# 생성된 코드가 원본(SQL, API 명세)과 맞는지 확인한다 (CI). API 코드는 bin/gen에 새로 만들어 비교한다.
gen-check:
	$(SQLC) diff
	@mkdir -p $(BIN)/gen
	$(OAPI) -config api/oapi-codegen.yaml -o $(BIN)/gen/api.gen.go api/openapi.yaml
	@diff -u $(OAPI_OUT) $(BIN)/gen/api.gen.go || { echo "$(OAPI_OUT) is out of date: run make gen"; exit 1; }
	@cd web && $(NPM) run --silent gen:api:check || { echo "web/src/api/schema.d.ts is out of date: run make gen"; exit 1; }

web-install:
	cd web && $(NPM) ci

web-build:
	cd web && $(NPM) run build

web-test:
	cd web && $(NPM) test

web-lint:
	cd web && $(NPM) run lint && $(NPM) run format:check

fmt:
	gofmt -w cmd internal migrations
	cd web && $(NPM) run format

db-up:
	docker run -d --name $(DB_CONTAINER) -p 5432:5432 \
		-e POSTGRES_USER=gpupool -e POSTGRES_PASSWORD=gpupool -e POSTGRES_DB=gpupool $(DB_IMAGE)

db-down:
	docker rm -f $(DB_CONTAINER)

db-migrate:
	$(MIGRATE) -path migrations -database "$(subst postgres://,pgx5://,$(LOCAL_DATABASE_URL))" up

db-psql:
	docker exec -it $(DB_CONTAINER) psql -U gpupool gpupool

# kind 클러스터 (노드 1대). 3단계(K8s Node·Pod 수집) 확인용.
kind-up:
	mkdir -p $(dir $(KIND_KUBECONFIG))
	$(KIND) create cluster --name $(KIND_CLUSTER) --kubeconfig $(KIND_KUBECONFIG)

# kind 노드를 GKE의 A100 MIG 노드(1g.5gb × 7)처럼 보이게 한다: GKE GPU 라벨과 임시 nvidia.com/gpu 7개.
# 실제 GPU는 없다.
kind-fake-gpu:
	$(KUBECTL) label node $(KIND_NODE) --overwrite \
		cloud.google.com/gke-accelerator=nvidia-tesla-a100 cloud.google.com/gke-gpu-partition-size=1g.5gb
	$(KUBECTL) patch node $(KIND_NODE) --subresource=status --type=json \
		-p '[{"op":"add","path":"/status/capacity/nvidia.com~1gpu","value":"7"}]'

kind-down:
	$(KIND) delete cluster --name $(KIND_CLUSTER) --kubeconfig $(KIND_KUBECONFIG)

# 로컬 실행: 서버1은 :8081, 서버2는 :8080. 화면 개발 서버는 /api와 /healthz를 :8080으로 넘긴다.
run-collector:
	HTTP_ADDR=:8081 DATABASE_URL="$(LOCAL_DATABASE_URL)" $(GO) run ./cmd/collector

# 서버1 + kind 클러스터의 Node·Pod 수집
run-collector-kind:
	HTTP_ADDR=:8081 DATABASE_URL="$(LOCAL_DATABASE_URL)" K8S_WATCH=true KUBECONFIG=$(KIND_KUBECONFIG) $(GO) run ./cmd/collector

run-api:
	HTTP_ADDR=:8080 DATABASE_URL="$(LOCAL_DATABASE_URL)" $(GO) run ./cmd/api

run-web:
	cd web && $(NPM) run dev

# 서버2 없이 목업 API(MSW)로 화면만 띄운다.
run-web-mock:
	cd web && $(NPM) run dev:mock

clean:
	rm -rf $(BIN) web/dist
