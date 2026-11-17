# 서버1(수집 서비스) 이미지. 레포 루트를 컨텍스트로 빌드한다.
#   docker build -f cmd/collector/Dockerfile -t gpu-pool/collector:dev .
FROM golang:1.26.4 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/collector ./cmd/collector

# 마이그레이션 SQL은 바이너리에 포함돼 있어 파일을 따로 넣지 않는다.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/collector /collector
# distroless의 nonroot 사용자(65532). 숫자로 적어야 K8s의 runAsNonRoot 검사가 통과한다.
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/collector"]
