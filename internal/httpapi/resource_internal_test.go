package httpapi

import (
	"encoding/json"
	"testing"
)

// 나누지 않은 GPU(full)의 nodeSelector는 JSON에서 null이 아니라 {}여야 한다(명세의 필수 객체).
// fake.Demo에는 나누지 않은 GPU가 없어 HTTP 테스트로는 이 경우를 지나지 않는다.
func TestPodNodeSelector(t *testing.T) {
	for profile, want := range map[string]string{
		"full":   `{}`,
		"1g.5gb": `{"cloud.google.com/gke-gpu-partition-size":"1g.5gb"}`,
	} {
		b, err := json.Marshal(podNodeSelector(profile))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != want {
			t.Errorf("podNodeSelector(%q) = %s, want %s", profile, b, want)
		}
		if podResourceName(profile) != "nvidia.com/gpu" {
			t.Errorf("podResourceName(%q) = %s", profile, podResourceName(profile))
		}
	}
}
