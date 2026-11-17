package metrics

import "strings"

// 모델별 MIG 최대 조각 수. DCGM은 이 값을 주지 않아 고정표로 둔다 (설계도 6장).
// DCGM의 modelName 라벨에 아래 이름이 들어 있으면 그 값을 쓴다. 위에서부터 먼저 맞는 것을 쓴다.
var sliceTable = []struct {
	model  string
	slices int
}{
	{"RTX PRO 6000", 4},
	{"RTX PRO 4500", 2},
	{"GB300", 7},
	{"GB200", 7},
	{"B300", 7},
	{"B200", 7},
	{"H200", 7},
	{"H100", 7},
	{"A100", 7},
	{"A30", 4},
}

// TotalSlices는 GPU 모델의 MIG 최대 조각 수를 돌려준다. 표에 없는 모델(MIG 미지원 포함)은 0이다.
func TotalSlices(model string) int {
	for _, e := range sliceTable {
		if strings.Contains(model, e.model) {
			return e.slices
		}
	}
	return 0
}
