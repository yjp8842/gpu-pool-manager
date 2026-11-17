package collector

import (
	"testing"

	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
)

func TestLayoutString(t *testing.T) {
	tests := []struct {
		profiles []string
		want     string
	}{
		{nil, "조각 없음"},
		{[]string{"full"}, "나누지 않음"},
		{[]string{"1g.23gb", "1g.23gb"}, "1g.23gb×2"},
		{[]string{"2g.45gb", "1g.23gb", "1g.23gb"}, "1g.23gb×2, 2g.45gb×1"},
	}
	for _, tt := range tests {
		l := layout{}
		for _, p := range tt.profiles {
			l.add(p)
		}
		if got := l.String(); got != tt.want {
			t.Errorf("layout%v = %q, want %q", tt.profiles, got, tt.want)
		}
	}
}

func TestHealthFromXID(t *testing.T) {
	for xid, want := range map[int]string{
		0:  ledger.HealthOK,
		13: ledger.HealthOK, // 애플리케이션 오류
		43: ledger.HealthOK,
		48: ledger.HealthUnhealthy,
		79: ledger.HealthUnhealthy,
		95: ledger.HealthUnhealthy,
	} {
		if got := healthFromXID(xid); got != want {
			t.Errorf("healthFromXID(%d) = %s, want %s", xid, got, want)
		}
	}
}
