// Package metrics는 GPU·MIG 관측 값을 읽어 오는 MetricSource와 그 결과인 스냅샷 타입을 정의한다.
// 구현은 하위 패키지 dcgm(실장비)과 fake(로컬·테스트)에 둔다.
package metrics

import (
	"context"
	"time"
)

// GPU는 물리 GPU 카드 한 장이다.
type GPU struct {
	UUID        string
	Index       int
	Model       string
	MemoryMB    int
	MIGEnabled  *bool // 메트릭에 없으면 nil (MIG 상태 알 수 없음)
	TotalSlices int   // 모델별 고정표에서 채운다. 예: A100·B200 7, RTX PRO 6000 4. 모르면 0
}

// PodRef는 조각을 쓰고 있는 것으로 관측된 컨테이너다.
type PodRef struct {
	Namespace string
	Name      string
	Container string
}

// Instance는 할당 단위 하나다. MIG 조각이거나, 나누지 않은 GPU 전체다.
type Instance struct {
	ParentUUID string
	GIID       int    // 나누지 않은 GPU는 0
	Profile    string // 예: "1g.5gb". 나누지 않은 GPU는 ProfileFull
	MIGUUID    string // 다시 나누면 바뀌므로 장부 키로 쓰지 않는다. 나누지 않은 GPU는 ""
	MemoryMB   int    // 조각 메모리. 모르면 0
	Pod        *PodRef
}

// ProfileFull은 나누지 않은 GPU 전체를 가리키는 프로파일 이름이다.
const ProfileFull = "full"

// Snapshot은 노드 하나를 한 번 읽은 결과다.
type Snapshot struct {
	Node      string
	GPUs      []GPU
	Instances []Instance
	LastXID   map[string]int // GPU UUID → 마지막 XID 오류 코드
	At        time.Time
}

// MetricSource는 노드별 스냅샷을 돌려준다. 수집 주기는 호출하는 쪽이 정한다.
//
// 스냅샷 하나는 그 노드의 전체 목록이다. 스냅샷에 없는 GPU·조각은 이번 회차에 보이지 않은 것으로 센다.
// 일부 노드를 읽지 못하면 읽은 노드의 스냅샷과 함께 오류를 돌려준다. 스냅샷이 없는 노드는
// 이번 회차에서 판단하지 않는다(미관측으로 세지 않는다). 일시적인 수집 실패를 고장으로 보지 않기 위해서다.
type MetricSource interface {
	Fetch(ctx context.Context) ([]Snapshot, error)
}
