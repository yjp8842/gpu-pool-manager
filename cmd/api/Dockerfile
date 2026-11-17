# 서버2(조회·예약 서비스) 이미지. 레포 루트를 컨텍스트로 빌드한다.
#   docker build -f cmd/api/Dockerfile -t gpu-pool/api:dev .
# 빌드 단계는 빌드하는 쪽 아키텍처로 돌리고 Go가 교차 컴파일한다. 그래서 Apple Silicon에서
# docker build --platform linux/amd64 를 해도 에뮬레이션 없이 GKE(amd64)용 이미지가 나온다.
FROM --platform=$BUILDPLATFORM golang:1.26.4 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /api
# distroless의 nonroot 사용자(65532). 숫자로 적어야 K8s의 runAsNonRoot 검사가 통과한다.
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/api"]
