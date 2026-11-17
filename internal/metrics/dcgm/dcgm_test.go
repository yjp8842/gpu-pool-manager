package dcgm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yjp8842/gpu-pool-manager/internal/metrics"
)

var t0 = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

const (
	migUUID = "GPU-5e2b7c90-1d3a-4f6e-9b31-0c2d8e1f4a00"
	migNode = "gke-grim-demo-gpu-mig-5f2c"
	a100    = "NVIDIA A100-SXM4-40GB"
)

func parseFile(t *testing.T, name, node string) []metrics.Snapshot {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	snaps, err := Parse(f, node, t0)
	if err != nil {
		t.Fatal(err)
	}
	return snaps
}

// wantMIG는 testdata/a100-mig-*.txt에서 나와야 하는 스냅샷이다: A100 1장, 1g.5gb × 7, 조각 두 개를 Pod가 쓴다.
func wantMIG() metrics.Snapshot {
	s := metrics.Snapshot{
		Node: migNode,
		GPUs: []metrics.GPU{{
			UUID: migUUID, Index: 0, Model: a100,
			MemoryMB:    0, // MIG 노드는 GPU 단위 메모리 줄이 없다
			MIGEnabled:  new(true),
			TotalSlices: 7,
		}},
		LastXID: map[string]int{migUUID: 0},
		At:      t0,
	}
	for gi := 7; gi <= 13; gi++ {
		s.Instances = append(s.Instances, metrics.Instance{ParentUUID: migUUID, GIID: gi, Profile: "1g.5gb", MemoryMB: 4864})
	}
	s.Instances[0].Pod = &metrics.PodRef{Namespace: "default", Name: "train-0", Container: "main"}
	s.Instances[1].Pod = &metrics.PodRef{Namespace: "default", Name: "train-1", Container: "main"}
	return s
}

func TestParseMIGNode(t *testing.T) {
	// 4.8까지의 이름(FB_USED MiB, XID_ERRORS, Hostname)과 4.9부터의 이름(FB_USED_BYTES, XID_ERROR, hostname)이
	// 같은 스냅샷이 되어야 한다.
	for _, file := range []string{"a100-mig-v4.8.txt", "a100-mig-v4.9.txt"} {
		t.Run(file, func(t *testing.T) {
			snaps := parseFile(t, file, "")
			if len(snaps) != 1 {
				t.Fatalf("snapshots = %d, want 1", len(snaps))
			}
			if want := wantMIG(); !reflect.DeepEqual(snaps[0], want) {
				t.Fatalf("snapshot =\n%+v\nwant\n%+v", snaps[0], want)
			}
		})
	}
}

func TestParseFullGPUNode(t *testing.T) {
	snaps := parseFile(t, "a100-full-v4.8.txt", "")
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want 1 (NVSwitch 줄은 건너뛴다)", len(snaps))
	}
	s := snaps[0]
	if s.Node != "gke-grim-demo-gpu-full-9a1d" || len(s.GPUs) != 2 || len(s.Instances) != 2 {
		t.Fatalf("snapshot = %+v", s)
	}
	for i, g := range s.GPUs {
		// 메모리 = 사용 + 남음 + 예약, MIG 모드는 메트릭 값(0 = 꺼짐)
		if g.Index != i || g.MemoryMB != 40960 || g.MIGEnabled == nil || *g.MIGEnabled || g.TotalSlices != 7 {
			t.Errorf("GPU %d = %+v", i, g)
		}
		in := s.Instances[i]
		if in.ParentUUID != g.UUID || in.Profile != metrics.ProfileFull || in.GIID != 0 || in.MemoryMB != 40960 {
			t.Errorf("GPU %d instance = %+v", i, in)
		}
	}
	if s.Instances[0].Pod != nil || s.Instances[1].Pod == nil || s.Instances[1].Pod.Name != "llm-serve-0" {
		t.Fatalf("pods = %+v, %+v", s.Instances[0].Pod, s.Instances[1].Pod)
	}
	if s.LastXID[s.GPUs[0].UUID] != 79 || s.LastXID[s.GPUs[1].UUID] != 0 {
		t.Fatalf("LastXID = %v", s.LastXID)
	}
}

func TestParseNodeName(t *testing.T) {
	// 엔드포인트에 노드 이름을 적으면 라벨보다 그 값을 쓴다.
	if snaps := parseFile(t, "a100-mig-v4.8.txt", "gpu-mig-1"); snaps[0].Node != "gpu-mig-1" {
		t.Fatalf("node = %q, want gpu-mig-1", snaps[0].Node)
	}

	// 라벨도 없고 엔드포인트에도 없으면 오류다.
	text := `DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-x",modelName="NVIDIA A100-SXM4-40GB"} 30` + "\n"
	if _, err := Parse(strings.NewReader(text), "", t0); !errors.Is(err, ErrNoNode) {
		t.Fatalf("err = %v, want ErrNoNode", err)
	}
}

func TestParseMIGModeWithoutInstances(t *testing.T) {
	// MIG를 켰는데 나눈 조각이 없으면 할당 단위가 없다. 나누지 않은 GPU(full)로 보지 않는다.
	text := `DCGM_FI_DEV_MIG_MODE{gpu="0",UUID="GPU-x",modelName="NVIDIA A100-SXM4-40GB",Hostname="n1"} 1` + "\n" +
		`DCGM_FI_DEV_FB_FREE{gpu="0",UUID="GPU-x",modelName="NVIDIA A100-SXM4-40GB",Hostname="n1"} 40000` + "\n"
	snaps, err := Parse(strings.NewReader(text), "", t0)
	if err != nil {
		t.Fatal(err)
	}
	if g := snaps[0].GPUs[0]; g.MIGEnabled == nil || !*g.MIGEnabled || len(snaps[0].Instances) != 0 {
		t.Fatalf("snapshot = %+v", snaps[0])
	}
	// MIG 모드를 모르고 조각도 없으면 나누지 않은 GPU다. 메모리는 사용·남음이 다 와야 안다.
	text = `DCGM_FI_DEV_FB_FREE{gpu="0",UUID="GPU-x",modelName="NVIDIA A100-SXM4-40GB",Hostname="n1"} 40000` + "\n"
	snaps, err = Parse(strings.NewReader(text), "", t0)
	if err != nil {
		t.Fatal(err)
	}
	if g, in := snaps[0].GPUs[0], snaps[0].Instances; g.MIGEnabled != nil || g.MemoryMB != 0 || len(in) != 1 || in[0].Profile != metrics.ProfileFull {
		t.Fatalf("snapshot = %+v", snaps[0])
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	for name, text := range map[string]string{
		"not metrics":   "this is not prometheus text {\n",
		"bad gpu index": `DCGM_FI_DEV_GPU_TEMP{gpu="zero",UUID="GPU-x",Hostname="n1"} 30` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(text), "", t0); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func TestParseEndpoints(t *testing.T) {
	got, err := ParseEndpoints([]string{"http://10.0.0.5:9400/metrics", "gpu-mig-1=http://10.0.0.6:9400/metrics?x=1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Endpoint{{URL: "http://10.0.0.5:9400/metrics"}, {Node: "gpu-mig-1", URL: "http://10.0.0.6:9400/metrics?x=1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("endpoints = %+v, want %+v", got, want)
	}
	for _, bad := range []string{"10.0.0.5:9400", "ftp://a/metrics", "=http://a/metrics", "n1="} {
		if _, err := ParseEndpoints([]string{bad}); err == nil {
			t.Errorf("ParseEndpoints(%q) accepted", bad)
		}
	}
}

func TestFetch(t *testing.T) {
	body, err := os.ReadFile("testdata/a100-mig-v4.9.txt")
	if err != nil {
		t.Fatal(err)
	}
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/plain" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write(body)
	}))
	defer ok.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "dcgm not ready", http.StatusServiceUnavailable)
	}))
	defer broken.Close()

	src := New([]Endpoint{{URL: ok.URL}, {Node: "gpu-down", URL: broken.URL}}, nil)
	src.now = func() time.Time { return t0 }

	// 읽은 노드의 스냅샷과 읽지 못한 엔드포인트의 오류를 함께 돌려준다.
	snaps, err := src.Fetch(t.Context())
	if err == nil || !strings.Contains(err.Error(), broken.URL) || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want the broken endpoint's 503", err)
	}
	if len(snaps) != 1 || !reflect.DeepEqual(snaps[0], wantMIG()) {
		t.Fatalf("snapshots = %+v", snaps)
	}
}
