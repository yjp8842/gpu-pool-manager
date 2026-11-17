package collector

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yjp8842/gpu-pool-manager/internal/kube"
	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
)

func (e *env) syncKube(snap kube.Snapshot) KubeStats {
	e.t.Helper()
	e.round++
	st, err := e.c.SyncKube(e.t.Context(), snap, t0.Add(time.Duration(e.round)*time.Minute))
	if err != nil {
		e.t.Fatalf("SyncKube round %d: %v", e.round, err)
	}
	return st
}

func (e *env) workloads() []ledger.Workload {
	e.t.Helper()
	ws, err := e.store.ListWorkloads(e.t.Context())
	if err != nil {
		e.t.Fatal(err)
	}
	return ws
}

func clusterSnapshot() kube.Snapshot {
	return kube.Snapshot{
		Nodes: []kube.Node{
			{Name: "gpu-a", Ready: true, InstanceType: "a2-highgpu-1g", HasGPU: true},
			{Name: "gpu-b", Ready: true, InstanceType: "a2-highgpu-2g", HasGPU: true},
			{Name: "plain", Ready: true},
		},
		Workloads: []kube.Workload{
			{Namespace: "default", Name: "train-0", UID: "u1", NodeName: "gpu-a", Phase: "Running"},
			{Namespace: "default", Name: "pending-0", UID: "u2", Phase: "Pending"},
			{Namespace: "ml", Name: "infer-0", UID: "u3", NodeName: "gpu-b", Phase: "Running"},
		},
	}
}

func TestSyncKubeWritesClusterState(t *testing.T) {
	e := newEnv(t)
	st := e.syncKube(clusterSnapshot())
	if st.Nodes != 2 || st.Workloads != 3 {
		t.Fatalf("stats = %+v", st)
	}

	// GPU가 없는 노드는 넣지 않는다.
	nodes, err := e.store.ListNodes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].Name != "gpu-a" || nodes[1].Name != "gpu-b" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if !*nodes[0].Ready || *nodes[0].InstanceType != "a2-highgpu-1g" {
		t.Fatalf("gpu-a = %+v", nodes[0])
	}

	ws := e.workloads()
	if len(ws) != 3 {
		t.Fatalf("workloads = %+v", ws)
	}
	byName := map[string]ledger.Workload{}
	for _, w := range ws {
		byName[w.Name] = w
	}
	if w := byName["train-0"]; w.Kind != ledger.KindPod || *w.NodeName != "gpu-a" || *w.UID != "u1" || w.Phase != "Running" {
		t.Fatalf("train-0 = %+v", w)
	}
	if w := byName["pending-0"]; w.NodeName != nil {
		t.Fatalf("pending-0 = %+v, want no node", w)
	}
}

func TestSyncKubeRemovesWhatIsGone(t *testing.T) {
	e := newEnv(t)
	e.syncKube(clusterSnapshot())

	// Pod 두 개가 지워지고, gpu-b 노드가 클러스터에서 빠졌다.
	snap := clusterSnapshot()
	snap.Nodes = snap.Nodes[:1]
	snap.Workloads = snap.Workloads[:1]

	st := e.syncKube(snap)
	if st.DeletedNodes != 1 || st.DeletedWorkloads != 2 {
		t.Fatalf("stats = %+v", st)
	}
	if ws := e.workloads(); len(ws) != 1 {
		t.Fatalf("workloads = %+v", ws)
	}
	if _, err := e.store.GetNode(t.Context(), "gpu-b"); err == nil {
		t.Fatal("gpu-b still in ledger after leaving the cluster")
	}

	// 같은 상태를 다시 반영하면 바뀌는 것이 없다.
	if st := e.syncKube(snap); st.changed() {
		t.Fatalf("second sync changed something: %+v", st)
	}
}

func TestSyncKubeKeepsDCGMOnlyNodes(t *testing.T) {
	e := newEnv(t)

	// DCGM으로 먼저 들어온 노드 두 개. node-b는 K8s에도 있고 GPU 라벨이 아직 없다.
	e.collect(twoNodes()...)
	snap := kube.Snapshot{Nodes: []kube.Node{{Name: "node-b", Ready: false}}}
	if st := e.syncKube(snap); st.Nodes != 1 || st.DeletedNodes != 0 {
		t.Fatalf("stats = %+v", st)
	}

	// K8s가 아는 노드는 K8s 값으로 갱신된다.
	n, err := e.store.GetNode(t.Context(), "node-b")
	if err != nil {
		t.Fatal(err)
	}
	if n.Ready == nil || *n.Ready {
		t.Fatalf("node-b ready = %v, want false from K8s", n.Ready)
	}
	// K8s가 모르는 노드(DCGM에서만 보임)는 지우지 않는다. 카드와 조각도 그대로다.
	if _, err := e.store.GetNode(t.Context(), "node-a"); err != nil {
		t.Fatalf("node-a removed: %v", err)
	}
	if ins := e.instances("node-a"); len(ins) != 7 {
		t.Fatalf("node-a instances = %d, want 7", len(ins))
	}

	// node-b가 클러스터에서 빠지면 K8s가 알던 노드이므로 지운다.
	if st := e.syncKube(kube.Snapshot{}); st.DeletedNodes != 1 {
		t.Fatalf("stats = %+v, want node-b deleted", st)
	}
	if _, ok := e.device("GPU-fake-node-b-0"); ok {
		t.Fatal("node-b GPU left after its node was deleted")
	}
}

// fakeKube는 RunKube 테스트용 KubeSource다.
type fakeKube struct {
	mu      sync.Mutex
	snap    kube.Snapshot
	changes chan struct{}
}

func (f *fakeKube) Changes() <-chan struct{} { return f.changes }

func (f *fakeKube) Snapshot() (kube.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap, nil
}

func (f *fakeKube) set(s kube.Snapshot) {
	f.mu.Lock()
	f.snap = s
	f.mu.Unlock()
	select {
	case f.changes <- struct{}{}:
	default:
	}
}

func TestRunKubeReactsToChanges(t *testing.T) {
	e := newEnv(t)
	src := &fakeKube{snap: clusterSnapshot(), changes: make(chan struct{}, 1)}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		e.c.RunKube(ctx, src, time.Hour) // 주기 반영은 일어나지 않고 변경 신호로만 반영된다
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitForWorkloads(t, e, 3)
	snap := clusterSnapshot()
	snap.Workloads = snap.Workloads[:1]
	src.set(snap)
	waitForWorkloads(t, e, 1)
}

func waitForWorkloads(t *testing.T, e *env, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ws, err := e.store.ListWorkloads(t.Context())
		if err == nil && len(ws) == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("workloads = %d, want %d within 5s", len(ws), n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
