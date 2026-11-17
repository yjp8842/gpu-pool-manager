// Command collector는 서버1(수집 서비스)이다.
// dcgm-exporter와 K8s API를 읽어 장부의 관측 값을 갱신한다. 할당 상태는 바꾸지 않는다.
//
// GPU·MIG는 METRIC_SOURCE(fake 또는 dcgm)에서, 노드·Pod는 K8S_WATCH=true일 때 K8s API에서 읽는다.
// dcgm이면 DCGM_ENDPOINTS의 dcgm-exporter /metrics를 읽는다.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/yjp8842/gpu-pool-manager/internal/collector"
	"github.com/yjp8842/gpu-pool-manager/internal/config"
	"github.com/yjp8842/gpu-pool-manager/internal/kube"
	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
	"github.com/yjp8842/gpu-pool-manager/internal/metrics"
	"github.com/yjp8842/gpu-pool-manager/internal/metrics/dcgm"
	"github.com/yjp8842/gpu-pool-manager/internal/metrics/fake"
	"github.com/yjp8842/gpu-pool-manager/internal/serve"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "collector")
	if err := run(logger); err != nil {
		logger.Error("collector stopped", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	source, err := newSource(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting",
		"metric_source", cfg.MetricSource,
		"scrape_interval", cfg.ScrapeInterval.String(),
		"k8s_watch", cfg.K8sWatch,
	)

	// 마이그레이션은 시작할 때 적용한다. 이미 최신이면 아무것도 하지 않는다.
	// 클러스터에서 별도 Job으로 돌릴지는 8단계에서 정한다.
	if err := ledger.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}
	store, err := ledger.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	c := collector.New(source, store, logger)
	var wg sync.WaitGroup
	wg.Go(func() { c.Run(ctx, cfg.ScrapeInterval) })

	if cfg.K8sWatch {
		w, err := startKubeWatch(ctx, logger)
		if err != nil {
			stop()
			wg.Wait()
			return err
		}
		wg.Go(func() { c.RunKube(ctx, w, cfg.ScrapeInterval) })
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", serve.Healthz)
	serveErr := serve.Run(ctx, logger, cfg.HTTPAddr, mux)

	// HTTP 서버가 먼저 실패해도 수집 루프를 멈추고 기다린 뒤 끝낸다.
	stop()
	wg.Wait()
	return serveErr
}

// startKubeWatch는 K8s에 연결해 Node·Pod informer를 띄운다.
func startKubeWatch(ctx context.Context, logger *slog.Logger) (*kube.Watcher, error) {
	kc, err := kube.Connect()
	if err != nil {
		return nil, err
	}
	w, err := kube.NewWatcher(kc, kubeResync)
	if err != nil {
		return nil, err
	}
	if err := w.Start(ctx, kubeSyncTimeout); err != nil {
		return nil, fmt.Errorf("kube: start watch: %w", err)
	}
	logger.Info("k8s watch started")
	return w, nil
}

const (
	// kubeResync는 informer가 캐시 전체를 다시 알리는 주기다. 놓친 변경이 있어도 이 주기 안에 반영된다.
	kubeResync = 10 * time.Minute
	// kubeSyncTimeout은 시작할 때 처음 목록을 받기까지 기다리는 시간이다. 넘으면 서버1을 끝낸다.
	kubeSyncTimeout = time.Minute
)

func newSource(cfg config.Config) (metrics.MetricSource, error) {
	switch cfg.MetricSource {
	case config.MetricSourceFake:
		return fake.New(fake.Demo()...), nil
	case config.MetricSourceDCGM:
		eps, err := dcgm.ParseEndpoints(cfg.DCGMEndpoints)
		if err != nil {
			return nil, err
		}
		return dcgm.New(eps, nil), nil
	default:
		return nil, fmt.Errorf("unknown METRIC_SOURCE %q", cfg.MetricSource)
	}
}
