// Package kube는 K8s API에서 노드와 GPU Pod 상태를 읽는다.
//
// Build는 K8s 객체 목록을 장부에 넣을 형태(Snapshot)로 바꾸는 순수 함수이고,
// Watcher는 informer로 객체를 계속 받아 두었다가 Snapshot을 만든다.
// 장부에 쓰는 일은 collector가 한다. 이 패키지는 장부를 모른다.
package kube

import (
	"cmp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// 라벨과 자원 이름.
const (
	labelGKEAccelerator = "cloud.google.com/gke-accelerator" // GKE가 GPU 노드에 붙인다 (예: nvidia-tesla-a100)
	labelGPUPresent     = "nvidia.com/gpu.present"           // GPU Operator가 GPU 노드에 붙인다
	labelInstanceType   = "node.kubernetes.io/instance-type"
	gpuResourcePrefix   = "nvidia.com/"
)

// Node는 K8s 노드 하나다.
type Node struct {
	Name         string
	Ready        bool
	InstanceType string
	HasGPU       bool // GPU 노드 표시(라벨)나 nvidia.com/ 자원이 있음
}

// Workload는 GPU(nvidia.com/ 자원)를 요청한 Pod 하나다.
type Workload struct {
	Namespace string
	Name      string
	UID       string
	NodeName  string // 아직 노드가 정해지지 않았으면 ""
	Phase     string // Pod phase. 삭제 중이면 Terminating
}

// Snapshot은 한 시점의 클러스터 상태다. Nodes는 GPU가 없는 노드도 모두 담는다.
type Snapshot struct {
	Nodes     []Node
	Workloads []Workload
}

// Build는 K8s 객체 목록으로 Snapshot을 만든다. Pod는 nvidia.com/ 자원을 요청한 것만 담는다.
func Build(nodes []*corev1.Node, pods []*corev1.Pod) Snapshot {
	var snap Snapshot
	for _, p := range pods {
		if len(gpuRequests(p)) == 0 {
			continue
		}
		snap.Workloads = append(snap.Workloads, Workload{
			Namespace: p.Namespace,
			Name:      p.Name,
			UID:       string(p.UID),
			NodeName:  p.Spec.NodeName,
			Phase:     podPhase(p),
		})
	}
	for _, n := range nodes {
		snap.Nodes = append(snap.Nodes, Node{
			Name:         n.Name,
			Ready:        nodeReady(n),
			InstanceType: n.Labels[labelInstanceType],
			HasGPU:       hasGPU(n),
		})
	}

	slices.SortFunc(snap.Nodes, func(a, b Node) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(snap.Workloads, func(a, b Workload) int {
		return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Name, b.Name))
	})
	return snap
}

// gpuRequests는 Pod 컨테이너가 요청한 nvidia.com/ 자원을 더한다. 요청이 없으면 limit을 쓴다
// (확장 자원은 limit만 적어도 요청이 같은 값이 된다). init 컨테이너는 보지 않는다.
func gpuRequests(p *corev1.Pod) map[string]int {
	out := map[string]int{}
	for _, c := range p.Spec.Containers {
		for name, q := range c.Resources.Limits {
			if strings.HasPrefix(string(name), gpuResourcePrefix) {
				if _, ok := c.Resources.Requests[name]; !ok {
					out[string(name)] += int(q.Value())
				}
			}
		}
		for name, q := range c.Resources.Requests {
			if strings.HasPrefix(string(name), gpuResourcePrefix) {
				out[string(name)] += int(q.Value())
			}
		}
	}
	for name, n := range out {
		if n <= 0 {
			delete(out, name)
		}
	}
	return out
}

func podPhase(p *corev1.Pod) string {
	if p.DeletionTimestamp != nil {
		return "Terminating"
	}
	return cmp.Or(string(p.Status.Phase), "Unknown")
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func hasGPU(n *corev1.Node) bool {
	if n.Labels[labelGKEAccelerator] != "" || n.Labels[labelGPUPresent] == "true" {
		return true
	}
	for name, q := range n.Status.Allocatable {
		if strings.HasPrefix(string(name), gpuResourcePrefix) && q.Value() > 0 {
			return true
		}
	}
	return false
}
