package kube

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func testNode(name string, ready bool, labels map[string]string, allocatable map[string]string) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}
	n.Status.Allocatable = corev1.ResourceList{}
	for k, v := range allocatable {
		n.Status.Allocatable[corev1.ResourceName(k)] = resource.MustParse(v)
	}
	return n
}

func testPod(ns, name, node string, phase corev1.PodPhase, limits map[string]string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name)},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{Phase: phase},
	}
	c := corev1.Container{Name: "main", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{}}}
	for k, v := range limits {
		c.Resources.Limits[corev1.ResourceName(k)] = resource.MustParse(v)
	}
	p.Spec.Containers = []corev1.Container{c}
	return p
}

func TestBuild(t *testing.T) {
	nodes := []*corev1.Node{
		// GKE MIG 노드 풀의 노드: GKE 라벨과 nvidia.com/gpu 7개 (1g.5gb 조각 7개)
		testNode("gpu-mig", true, map[string]string{
			labelGKEAccelerator: "nvidia-tesla-a100", "cloud.google.com/gke-gpu-partition-size": "1g.5gb",
			labelInstanceType: "a2-highgpu-1g",
		}, map[string]string{"nvidia.com/gpu": "7"}),
		testNode("gpu-op", true, map[string]string{labelGPUPresent: "true"}, nil), // GPU Operator 라벨만 있음
		testNode("gpu-res", true, nil, map[string]string{"nvidia.com/gpu": "1"}),  // 라벨 없이 자원만 있음
		testNode("plain", false, nil, map[string]string{"cpu": "4"}),
	}

	terminating := testPod("default", "train-old", "gpu-mig", corev1.PodRunning, map[string]string{"nvidia.com/gpu": "1"})
	terminating.DeletionTimestamp = &metav1.Time{}
	pods := []*corev1.Pod{
		testPod("default", "train-0", "gpu-mig", corev1.PodRunning, map[string]string{"nvidia.com/gpu": "1"}),
		testPod("default", "pending-0", "", corev1.PodPending, map[string]string{"nvidia.com/gpu": "1"}),
		testPod("default", "web", "plain", corev1.PodRunning, map[string]string{"cpu": "1"}), // GPU 없음
		terminating,
	}

	snap := Build(nodes, pods)

	wantNodes := []Node{
		{Name: "gpu-mig", Ready: true, InstanceType: "a2-highgpu-1g", HasGPU: true},
		{Name: "gpu-op", Ready: true, HasGPU: true},
		{Name: "gpu-res", Ready: true, HasGPU: true},
		{Name: "plain", Ready: false},
	}
	if !slices.Equal(snap.Nodes, wantNodes) {
		t.Fatalf("nodes = %+v, want %+v", snap.Nodes, wantNodes)
	}

	wantWorkloads := []Workload{
		{Namespace: "default", Name: "pending-0", UID: "uid-pending-0", Phase: "Pending"},
		{Namespace: "default", Name: "train-0", UID: "uid-train-0", NodeName: "gpu-mig", Phase: "Running"},
		{Namespace: "default", Name: "train-old", UID: "uid-train-old", NodeName: "gpu-mig", Phase: "Terminating"},
	}
	if !slices.Equal(snap.Workloads, wantWorkloads) {
		t.Fatalf("workloads = %+v, want %+v", snap.Workloads, wantWorkloads)
	}
}

func TestGPURequests(t *testing.T) {
	p := testPod("default", "p", "n1", corev1.PodRunning, map[string]string{"nvidia.com/gpu": "1", "cpu": "2"})
	// 두 번째 컨테이너는 요청과 limit을 함께 적었다. 요청 값을 쓴다.
	p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "side", Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("2")},
		Limits:   corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("2")},
	}})
	got := gpuRequests(p)
	if len(got) != 1 || got["nvidia.com/gpu"] != 3 {
		t.Fatalf("gpuRequests = %v, want nvidia.com/gpu: 3", got)
	}
}
