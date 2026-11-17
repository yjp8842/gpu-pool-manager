// Package migrations는 장부 테이블 SQL 마이그레이션을 실행 파일에 넣어 둔다.
// 파일 이름은 golang-migrate 규칙({버전}_{제목}.up.sql / .down.sql)을 따른다.
package migrations

import "embed"

// FS는 이 디렉터리의 SQL 마이그레이션 파일이다.
//
//go:embed *.sql
var FS embed.FS
