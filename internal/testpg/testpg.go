// Package testpg는 실제 PostgreSQL이 필요한 테스트를 돕는다. 테스트 코드에서만 쓴다.
//
// 패키지 테스트마다 PostgreSQL 컨테이너 하나를 띄우고(testcontainers), 마이그레이션을 적용한
// template DB를 테스트마다 복사해 준다. 테스트끼리 데이터가 섞이지 않는다.
// go test -short이면 컨테이너를 띄우지 않고 DB 테스트를 건너뛴다.
//
//	func TestMain(m *testing.M) { os.Exit(testpg.Main(m, ledger.Migrate)) }
package testpg

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	image      = "postgres:18-alpine"
	templateDB = "ledger_template"
)

var state struct {
	dsn   string        // template DB 주소
	admin *pgxpool.Pool // CREATE/DROP DATABASE용 (postgres DB에 연결)
	mu    sync.Mutex    // 같은 template을 동시에 복사하지 않도록
	seq   int
}

// Main은 PostgreSQL 컨테이너를 띄우고 setup(template DB 주소)으로 template을 준비한 뒤
// 테스트를 실행하고 종료 코드를 돌려준다. TestMain에서 부른다.
func Main(m *testing.M, setup func(dsn string) error) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	ctx := context.Background()

	ctr, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase(templateDB),
		tcpostgres.WithUsername("ledger"),
		tcpostgres.WithPassword("ledger"),
		tcpostgres.BasicWaitStrategies(),
	)
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			fmt.Fprintln(os.Stderr, "testpg: terminate container:", err)
		}
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testpg: start PostgreSQL container (Docker가 필요하다. -short이면 건너뛴다):", err)
		return 1
	}

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testpg: connection string:", err)
		return 1
	}
	if err := setup(dsn); err != nil {
		fmt.Fprintln(os.Stderr, "testpg: setup template:", err)
		return 1
	}
	admin, err := pgxpool.New(ctx, withDatabase(dsn, "postgres"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "testpg: admin pool:", err)
		return 1
	}
	defer admin.Close()

	state.dsn = dsn
	state.admin = admin
	return m.Run()
}

// NewDatabase는 template을 복사한 새 DB의 주소를 돌려준다. 테스트가 끝나면 DB를 지운다.
// -short이거나 Main을 거치지 않았으면 테스트를 건너뛴다.
func NewDatabase(t testing.TB) string {
	t.Helper()
	return createDatabase(t, templateDB)
}

// NewEmptyDatabase는 마이그레이션을 적용하지 않은 빈 DB의 주소를 돌려준다.
func NewEmptyDatabase(t testing.TB) string {
	t.Helper()
	return createDatabase(t, "template0")
}

func createDatabase(t testing.TB, template string) string {
	t.Helper()
	if state.admin == nil {
		t.Skip("PostgreSQL이 필요한 테스트 (-short에서는 건너뛴다)")
	}
	state.mu.Lock()
	state.seq++
	name := fmt.Sprintf("test_%d", state.seq)
	_, err := state.admin.Exec(t.Context(), "CREATE DATABASE "+name+" TEMPLATE "+template)
	state.mu.Unlock()
	if err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	// Cleanup은 나중에 등록한 것부터 실행된다. 이 DB에 연결한 쪽이 먼저 닫히고 DB가 지워진다.
	t.Cleanup(func() {
		if _, err := state.admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	return withDatabase(state.dsn, name)
}

func withDatabase(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}
