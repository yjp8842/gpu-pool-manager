package ledger

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound는 찾는 행이 없거나 조건부 갱신(BindReservation, CloseAllocation)에서 바뀐 행이 없을 때의 오류다.
// 생성된 쿼리가 돌려주는 pgx.ErrNoRows와 같아 errors.Is로 비교한다.
var ErrNotFound = pgx.ErrNoRows

// PostgreSQL SQLSTATE 코드.
const (
	codeForeignKeyViolation = "23503"
	codeUniqueViolation     = "23505"
	codeCheckViolation      = "23514"
)

// IsUniqueViolation은 err가 유일 제약 위반인지 알려준다. constraint가 비어 있으면 제약 이름은 보지 않는다.
func IsUniqueViolation(err error, constraint string) bool {
	return isViolation(err, codeUniqueViolation, constraint)
}

// IsCheckViolation은 err가 CHECK 제약 위반인지 알려준다. constraint가 비어 있으면 제약 이름은 보지 않는다.
func IsCheckViolation(err error, constraint string) bool {
	return isViolation(err, codeCheckViolation, constraint)
}

// IsForeignKeyViolation은 err가 참조 제약 위반인지 알려준다. constraint가 비어 있으면 제약 이름은 보지 않는다.
func IsForeignKeyViolation(err error, constraint string) bool {
	return isViolation(err, codeForeignKeyViolation, constraint)
}

func isViolation(err error, code, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}
