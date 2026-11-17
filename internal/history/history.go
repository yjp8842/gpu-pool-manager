// Package history는 이력(allocation_event)을 한 줄씩 추가한다. 호출한 쪽의 트랜잭션 안에서만 쓴다.
//
// 서버1의 collector가 구성 변경을 남기고(개발 단계 2), 서버2의 allocation 패키지가
// 예약·할당·반납·만료·취소·거부를 남긴다(개발 단계 5). 이력은 고치거나 지우지 않는다.
package history

import (
	"context"
	"fmt"
	"time"

	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
)

// MIGReconfigured는 GPU 한 장의 조각 구성이 before에서 after로 바뀐 것을 남긴다.
// before, after는 "1g.5gb×7" 같은 구성 요약이다.
func MIGReconfigured(ctx context.Context, q *ledger.Queries, at time.Time, node, gpuUUID, before, after string) error {
	_, err := q.AppendEvent(ctx, ledger.AppendEventParams{
		OccurredAt: at,
		Event:      ledger.EventMIGReconfigured,
		NodeName:   &node,
		GPUUUID:    &gpuUUID,
		Message:    new(before + " → " + after),
	})
	if err != nil {
		return fmt.Errorf("history: append %s: %w", ledger.EventMIGReconfigured, err)
	}
	return nil
}

// Instance는 이력에 남길 조각의 고정 키와 당시 MIG UUID다.
type Instance struct {
	GPUUUID string
	GIID    int32
	Profile string
	MIGUUID *string
}

// Allocation은 할당 행 a의 상태 변화(event)를 남긴다. 대상 자원은 a의 노드 + 프로파일이고,
// 조각에 묶였으면 in으로 그 조각을 함께 남긴다. message가 비어 있으면 남기지 않는다.
func Allocation(ctx context.Context, q *ledger.Queries, at time.Time, event string, a ledger.Allocation, in *Instance, message string) error {
	p := ledger.AppendEventParams{
		OccurredAt:        at,
		Event:             event,
		NodeName:          &a.NodeName,
		Profile:           &a.Profile,
		WorkloadKind:      &a.TargetKind,
		WorkloadNamespace: &a.TargetNamespace,
		WorkloadName:      &a.TargetName,
		Requester:         a.Requester,
		RequestID:         a.RequestID,
		AllocationID:      &a.ID,
	}
	if in != nil {
		p.GPUUUID, p.GIID, p.Profile, p.MIGUUID = &in.GPUUUID, &in.GIID, &in.Profile, in.MIGUUID
	}
	if message != "" {
		p.Message = &message
	}
	if _, err := q.AppendEvent(ctx, p); err != nil {
		return fmt.Errorf("history: append %s: %w", event, err)
	}
	return nil
}

// Request는 거부 이력에 남길 예약 요청이다.
type Request struct {
	Node      string
	Profile   string
	Kind      string
	Namespace string
	Name      string
	Requester string
	RequestID string
}

// Rejected는 남은 수량이 없어 거부한 예약 요청을 남긴다. 거부된 요청은 할당 행이 없다.
func Rejected(ctx context.Context, q *ledger.Queries, at time.Time, r Request, message string) error {
	_, err := q.AppendEvent(ctx, ledger.AppendEventParams{
		OccurredAt:        at,
		Event:             ledger.EventRejected,
		NodeName:          &r.Node,
		Profile:           &r.Profile,
		WorkloadKind:      &r.Kind,
		WorkloadNamespace: &r.Namespace,
		WorkloadName:      &r.Name,
		Requester:         &r.Requester,
		RequestID:         &r.RequestID,
		Message:           &message,
	})
	if err != nil {
		return fmt.Errorf("history: append %s: %w", ledger.EventRejected, err)
	}
	return nil
}
