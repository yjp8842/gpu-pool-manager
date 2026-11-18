// Package ledger는 장부 테이블 접근과 트랜잭션을 담당한다.
//
// 테이블은 migrations/의 SQL로 만들고, 쿼리 코드(*.sql.go, models.go, db.go)는
// queries/의 SQL에서 sqlc로 생성한다. 쿼리를 바꾸면 make gen으로 다시 생성한다.
//
// 테이블마다 쓰는 쪽이 정해져 있다. 서버1(collector)은 node, gpu_device, gpu_instance,
// workload의 관측 값만 쓰고, allocation은 서버2의 allocation 패키지만 쓴다.
// allocation_event는 추가만 하고 고치거나 지우지 않는다.
package ledger
