// Command api는 서버2(조회·예약 서비스)이다.
// 장부를 조회해 화면과 요청자에게 응답하고, 예약·확정·만료와 이력 기록을 맡는다.
//
// 조회 API(개발 단계 4)와 예약 API·정합 작업(개발 단계 5)을 함께 돌린다.
// 정합 작업은 RECONCILE_INTERVAL마다 서버1이 쓴 관측 값을 보고 확정·반납·만료·관측 할당을 처리한다.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/yjp8842/gpu-pool-manager/internal/allocation"
	"github.com/yjp8842/gpu-pool-manager/internal/config"
	"github.com/yjp8842/gpu-pool-manager/internal/httpapi"
	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
	"github.com/yjp8842/gpu-pool-manager/internal/serve"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "api")
	if err := run(logger); err != nil {
		logger.Error("api stopped", "err", err)
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting",
		"reconcile_interval", cfg.ReconcileInterval.String(),
		"reservation_ttl", cfg.ReservationTTL.String(),
	)

	// 서버1과 같이 시작할 때 마이그레이션을 적용한다. 동시에 떠도 golang-migrate 잠금으로 한 번만 적용된다.
	if err := ledger.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}
	store, err := ledger.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	alloc := allocation.New(store, allocation.Config{TTL: cfg.ReservationTTL, Logger: logger})
	var wg sync.WaitGroup
	wg.Go(func() { alloc.Run(ctx, cfg.ReconcileInterval) })

	serveErr := serve.Run(ctx, logger, cfg.HTTPAddr, httpapi.NewHandler(store, alloc, logger))
	// HTTP 서버가 먼저 실패해도 정합 작업을 멈추고 기다린 뒤 끝낸다.
	stop()
	wg.Wait()
	return serveErr
}
