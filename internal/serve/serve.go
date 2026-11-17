// Package serve는 두 서비스가 공통으로 쓰는 HTTP 서버 실행과 정상 종료를 담당한다.
package serve

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

const shutdownTimeout = 10 * time.Second

// Run은 ctx가 끝날 때까지 addr에서 handler를 서비스하고, 끝나면 진행 중인 요청을 기다려 종료한다.
func Run(ctx context.Context, logger *slog.Logger, addr string, handler http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	logger.Info("http server shutting down")
	return srv.Shutdown(shutdownCtx)
}

// Healthz는 프로세스가 살아 있으면 200을 돌려주는 핸들러다.
func Healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
