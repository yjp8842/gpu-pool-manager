package ledger

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"

	"github.com/yjp8842/gpu-pool-manager/internal/testpg"
)

var ledgerTables = []string{
	"node", "gpu_device", "gpu_instance", "workload", "allocation", "allocation_event",
}

func TestMigrateUpDownUp(t *testing.T) {
	dsn := testpg.NewEmptyDatabase(t)

	if err := Migrate(dsn); err != nil {
		t.Fatalf("up: %v", err)
	}
	if got := countTables(t, dsn); got != len(ledgerTables) {
		t.Fatalf("after up: %d ledger tables, want %d", got, len(ledgerTables))
	}

	// 이미 최신이면 아무것도 하지 않는다.
	if err := Migrate(dsn); err != nil {
		t.Fatalf("second up: %v", err)
	}

	m, err := newMigrate(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("down: %v", err)
	}
	if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
		t.Fatalf("close: %v %v", srcErr, dbErr)
	}
	if got := countTables(t, dsn); got != 0 {
		t.Fatalf("after down: %d ledger tables, want 0", got)
	}

	if err := Migrate(dsn); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if got := countTables(t, dsn); got != len(ledgerTables) {
		t.Fatalf("after up again: %d ledger tables, want %d", got, len(ledgerTables))
	}
}

func countTables(t *testing.T, dsn string) int {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var n int
	err = conn.QueryRow(t.Context(),
		"SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = ANY($1)",
		ledgerTables).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPgx5URL(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "postgres://u:secret@db:5432/gpupool?sslmode=disable", want: "pgx5://u:secret@db:5432/gpupool?sslmode=disable"},
		{in: "postgresql://db/gpupool", want: "pgx5://db/gpupool"},
		{in: "mysql://db/gpupool", wantErr: true},
		{in: "host=db user=u password=secret", wantErr: true},
		{in: "postgres://u:secret@db:bad-port/x", wantErr: true},
	}
	for _, tt := range tests {
		got, err := pgx5URL(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("pgx5URL(%q) = %q, want error", tt.in, got)
			} else if strings.Contains(err.Error(), "secret") {
				t.Errorf("pgx5URL(%q) error leaks password: %v", tt.in, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("pgx5URL(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}
