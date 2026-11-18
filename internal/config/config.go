// Package config는 서버1(collector)과 서버2(api)가 공통으로 쓰는 설정을 환경 변수에서 읽는다.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// 메트릭 출처. 로컬은 목업 메트릭, 실장비는 dcgm-exporter 직접 조회.
const (
	MetricSourceFake = "fake"
	MetricSourceDCGM = "dcgm"
)

// Config는 두 서비스의 실행 설정이다. 각 필드 옆 주석은 환경 변수 이름이다.
type Config struct {
	HTTPAddr          string        // HTTP_ADDR
	DatabaseURL       string        // DATABASE_URL
	MetricSource      string        // METRIC_SOURCE (fake | dcgm)
	DCGMEndpoints     []string      // DCGM_ENDPOINTS (쉼표로 구분)
	K8sWatch          bool          // K8S_WATCH (true면 서버1이 K8s에서 Node·Pod를 읽는다)
	ScrapeInterval    time.Duration // SCRAPE_INTERVAL
	ReconcileInterval time.Duration // RECONCILE_INTERVAL
	ReservationTTL    time.Duration // RESERVATION_TTL
}

// Defaults는 환경 변수가 없을 때 쓰는 값이다.
func Defaults() Config {
	return Config{
		HTTPAddr:          ":8080",
		MetricSource:      MetricSourceFake,
		ScrapeInterval:    15 * time.Second,
		ReconcileInterval: 5 * time.Second,
		ReservationTTL:    5 * time.Minute,
	}
}

// Load는 Defaults 위에 환경 변수를 덮어쓴 설정을 돌려준다.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	cfg := Defaults()

	if v, ok := lookup("HTTP_ADDR"); ok && v != "" {
		cfg.HTTPAddr = v
	}
	if v, ok := lookup("DATABASE_URL"); ok {
		cfg.DatabaseURL = v
	}
	if v, ok := lookup("METRIC_SOURCE"); ok && v != "" {
		cfg.MetricSource = v
	}
	if v, ok := lookup("DCGM_ENDPOINTS"); ok {
		cfg.DCGMEndpoints = splitList(v)
	}
	if v, ok := lookup("K8S_WATCH"); ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("K8S_WATCH: true 또는 false여야 합니다: %s", v)
		}
		cfg.K8sWatch = b
	}

	durations := []struct {
		name string
		dst  *time.Duration
	}{
		{"SCRAPE_INTERVAL", &cfg.ScrapeInterval},
		{"RECONCILE_INTERVAL", &cfg.ReconcileInterval},
		{"RESERVATION_TTL", &cfg.ReservationTTL},
	}
	for _, d := range durations {
		v, ok := lookup(d.name)
		if !ok || v == "" {
			continue
		}
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", d.name, err)
		}
		if parsed <= 0 {
			return Config{}, fmt.Errorf("%s: 0보다 커야 합니다: %s", d.name, v)
		}
		*d.dst = parsed
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	switch c.MetricSource {
	case MetricSourceFake:
	case MetricSourceDCGM:
		if len(c.DCGMEndpoints) == 0 {
			return fmt.Errorf("METRIC_SOURCE=%s 이면 DCGM_ENDPOINTS가 필요합니다", MetricSourceDCGM)
		}
	default:
		return fmt.Errorf("METRIC_SOURCE: 알 수 없는 값 %q (fake 또는 dcgm)", c.MetricSource)
	}
	return nil
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
