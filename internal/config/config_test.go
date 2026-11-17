package config

import (
	"reflect"
	"testing"
	"time"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(lookupFrom(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(cfg, Defaults()) {
		t.Fatalf("got %+v, want defaults %+v", cfg, Defaults())
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := load(lookupFrom(map[string]string{
		"HTTP_ADDR":          ":9090",
		"DATABASE_URL":       "postgres://localhost/pool",
		"METRIC_SOURCE":      "dcgm",
		"DCGM_ENDPOINTS":     "http://a:9400/metrics, http://b:9400/metrics,",
		"K8S_WATCH":          "true",
		"SCRAPE_INTERVAL":    "30s",
		"RECONCILE_INTERVAL": "2s",
		"RESERVATION_TTL":    "10m",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := Config{
		HTTPAddr:          ":9090",
		DatabaseURL:       "postgres://localhost/pool",
		MetricSource:      MetricSourceDCGM,
		DCGMEndpoints:     []string{"http://a:9400/metrics", "http://b:9400/metrics"},
		K8sWatch:          true,
		ScrapeInterval:    30 * time.Second,
		ReconcileInterval: 2 * time.Second,
		ReservationTTL:    10 * time.Minute,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"unknown metric source", map[string]string{"METRIC_SOURCE": "otel"}},
		{"dcgm without endpoints", map[string]string{"METRIC_SOURCE": "dcgm"}},
		{"bad duration", map[string]string{"SCRAPE_INTERVAL": "fifteen"}},
		{"zero duration", map[string]string{"RESERVATION_TTL": "0s"}},
		{"bad k8s watch", map[string]string{"K8S_WATCH": "yes please"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := load(lookupFrom(tt.env)); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}
