package collector

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yjp8842/gpu-pool-manager/internal/metrics"
)

// layout은 GPU 한 장의 조각 구성이다: 프로파일 → 조각 수.
type layout map[string]int

func (l layout) add(profile string) {
	l[profile]++
}

func (l layout) equal(o layout) bool {
	return maps.Equal(l, o)
}

// String은 이력에 남길 구성 요약이다. 예: "1g.5gb×7", "1g.5gb×4, 3g.20gb×1", "나누지 않음", "조각 없음".
func (l layout) String() string {
	if len(l) == 0 {
		return "조각 없음"
	}
	if len(l) == 1 && l[metrics.ProfileFull] == 1 {
		return "나누지 않음"
	}
	parts := make([]string, 0, len(l))
	for _, profile := range slices.Sorted(maps.Keys(l)) {
		parts = append(parts, fmt.Sprintf("%s×%d", profile, l[profile]))
	}
	return strings.Join(parts, ", ")
}
