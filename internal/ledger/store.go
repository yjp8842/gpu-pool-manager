package ledger

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store는 장부 DB 연결 풀과 생성된 쿼리를 묶는다. 트랜잭션 밖의 쿼리는 Store에서 바로 부른다.
type Store struct {
	*Queries
	pool *pgxpool.Pool
}

// Open은 databaseURL(postgres://...)로 연결 풀을 만들고 연결되는지 확인한다.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("ledger: open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ledger: ping: %w", err)
	}
	return &Store{Queries: New(pool), pool: pool}, nil
}

// Close는 연결 풀을 닫는다.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping은 DB에 연결되는지 확인한다.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// InTx는 fn을 한 트랜잭션 안에서 실행한다. fn이 오류를 돌려주면 롤백하고, 아니면 커밋한다.
// 남은 수량 확인과 예약 생성처럼 함께 바뀌어야 하는 쿼리는 이 안에서 부른다.
func (s *Store) InTx(ctx context.Context, fn func(q *Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(s.WithTx(tx))
	})
}
