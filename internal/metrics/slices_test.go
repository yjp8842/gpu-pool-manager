package metrics

import "testing"

func TestTotalSlices(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"NVIDIA B200", 7},
		{"NVIDIA A100-SXM4-40GB", 7},
		{"NVIDIA H100 80GB HBM3", 7},
		{"NVIDIA RTX PRO 6000 Blackwell Server Edition", 4},
		{"NVIDIA RTX PRO 4500 Blackwell Server Edition", 2},
		{"NVIDIA A30", 4},
		{"Tesla T4", 0},
		{"NVIDIA RTX 6000 Ada Generation", 0},
		{"", 0},
	}
	for _, tt := range tests {
		if got := TotalSlices(tt.model); got != tt.want {
			t.Errorf("TotalSlices(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}
