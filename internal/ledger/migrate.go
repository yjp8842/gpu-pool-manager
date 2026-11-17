package ledger

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // pgx5:// 드라이버 등록
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/yjp8842/gpu-pool-manager/migrations"
)

// Migrate는 migrations/의 SQL을 databaseURL의 DB에 마지막 버전까지 적용한다.
// 이미 최신이면 아무것도 하지 않는다. 여러 프로세스가 동시에 불러도 golang-migrate의 잠금으로 한 번만 적용된다.
func Migrate(databaseURL string) (err error) {
	m, err := newMigrate(databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if err == nil {
			err = errors.Join(srcErr, dbErr)
		}
	}()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("ledger: migrate up: %w", err)
	}
	return nil
}

func newMigrate(databaseURL string) (*migrate.Migrate, error) {
	dbURL, err := pgx5URL(databaseURL)
	if err != nil {
		return nil, err
	}
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("ledger: read migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dbURL)
	if err != nil {
		return nil, fmt.Errorf("ledger: connect for migration: %w", err)
	}
	return m, nil
}

// pgx5URL은 postgres:// 주소를 golang-migrate pgx/v5 드라이버가 쓰는 pgx5:// 주소로 바꾼다.
// 오류 메시지에는 주소를 넣지 않는다. 비밀번호가 로그에 남지 않게 하기 위해서다.
func pgx5URL(databaseURL string) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", errors.New("ledger: DATABASE_URL is not a valid URL")
	}
	switch u.Scheme {
	case "postgres", "postgresql":
		u.Scheme = "pgx5"
		return u.String(), nil
	default:
		return "", fmt.Errorf("ledger: DATABASE_URL must start with postgres:// or postgresql:// (got scheme %q)", u.Scheme)
	}
}
