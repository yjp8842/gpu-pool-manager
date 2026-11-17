package kube

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWatcherSnapshotAndChanges(t *testing.T) {
	kc := fake.NewClientset(
		testNode("gpu-mig", true, map[string]string{labelGKEAccelerator: "nvidia-tesla-a100"}, map[string]string{"nvidia.com/gpu": "7"}),
		testPod("default", "train-0", "gpu-mig", corev1.PodRunning, map[string]string{"nvidia.com/gpu": "1"}),
	)

	w, err := NewWatcher(kc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(t.Context(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	snap, err := w.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Nodes) != 1 || len(snap.Workloads) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	drain(w)

	// Pod를 하나 더 만들면 변경 신호가 오고 Snapshot에 나온다.
	pod := testPod("default", "train-1", "gpu-mig", corev1.PodPending, map[string]string{"nvidia.com/gpu": "1"})
	if _, err := kc.CoreV1().Pods("default").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitChange(t, w)
	waitFor(t, func() bool {
		snap, err := w.Snapshot()
		return err == nil && len(snap.Workloads) == 2
	})

	// Pod를 지우면 신호가 오고 Snapshot에서 빠진다.
	drain(w)
	if err := kc.CoreV1().Pods("default").Delete(t.Context(), "train-0", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitChange(t, w)
	waitFor(t, func() bool {
		snap, err := w.Snapshot()
		return err == nil && len(snap.Workloads) == 1 && snap.Workloads[0].Name == "train-1"
	})
}

func drain(w *Watcher) {
	select {
	case <-w.Changes():
	default:
	}
}

func waitChange(t *testing.T, w *Watcher) {
	t.Helper()
	select {
	case <-w.Changes():
	case <-time.After(5 * time.Second):
		t.Fatal("no change signal within 5s")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
