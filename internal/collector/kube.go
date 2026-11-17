package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/yjp8842/gpu-pool-manager/internal/kube"
	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
)

// KubeSource는 클러스터 상태를 주는 쪽이다. kube.Watcher가 구현한다.
type KubeSource interface {
	// Changes는 클러스터 상태가 바뀌면 신호를 보낸다.
	Changes() <-chan struct{}
	// Snapshot은 지금의 클러스터 상태다.
	Snapshot() (kube.Snapshot, error)
}

// KubeStats는 K8s 반영 한 번의 결과다.
type KubeStats struct {
	Nodes            int // 넣거나 갱신한 노드 수
	DeletedNodes     int // K8s에서 사라져 지운 노드 수
	Workloads        int
	DeletedWorkloads int
}

func (s KubeStats) changed() bool {
	return s.DeletedNodes > 0 || s.DeletedWorkloads > 0
}

// kubeDebounce는 변경 신호를 받은 뒤 반영하기 전에 기다리는 시간이다. Pod 하나가 뜰 때 여러 번
// 오는 변경을 한 번에 반영한다.
const kubeDebounce = 500 * time.Millisecond

// RunKube는 src의 변경 신호가 오거나 interval이 지날 때마다 클러스터 상태를 장부에 반영한다.
// ctx가 끝나면 돌아온다.
func (c *Collector) RunKube(ctx context.Context, src KubeSource, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	first := true
	for {
		c.syncKubeAndLog(ctx, src, first)
		first = false
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-src.Changes():
			select {
			case <-ctx.Done():
				return
			case <-time.After(kubeDebounce):
			}
		}
	}
}

func (c *Collector) syncKubeAndLog(ctx context.Context, src KubeSource, first bool) {
	snap, err := src.Snapshot()
	if err != nil {
		c.logger.Warn("kube snapshot", "err", err)
		return
	}
	st, err := c.SyncKube(ctx, snap, time.Now())
	if ctx.Err() != nil {
		return
	}
	attrs := []any{
		"nodes", st.Nodes, "deleted_nodes", st.DeletedNodes,
		"workloads", st.Workloads, "deleted_workloads", st.DeletedWorkloads,
	}
	switch {
	case err != nil:
		c.logger.Warn("kube sync failed", append(attrs, "err", err)...)
	case first || st.changed():
		c.logger.Info("kube sync", attrs...)
	default:
		c.logger.Debug("kube sync", attrs...)
	}
}

// SyncKube는 클러스터 상태를 장부에 한 트랜잭션으로 반영한다. informer 캐시는 클러스터 전체 목록이므로
// 미관측 횟수를 세지 않고, 목록에 없는 것은 바로 지운다.
//   - 노드: GPU 노드이거나 DCGM에서 먼저 들어온 노드만 넣고 갱신한다. K8s에서 사라진 노드는 지운다.
//     DCGM에서만 보인 노드(Ready가 NULL)는 K8s가 모르는 노드라 지우지 않는다.
//   - 워크로드: GPU를 요청한 Pod. 목록에 없는 것은 지운다.
func (c *Collector) SyncKube(ctx context.Context, snap kube.Snapshot, at time.Time) (KubeStats, error) {
	var st KubeStats
	err := c.store.InTx(ctx, func(q *ledger.Queries) error {
		st = KubeStats{}

		// 노드
		existing, err := q.ListNodes(ctx)
		if err != nil {
			return fmt.Errorf("list nodes: %w", err)
		}
		inLedger := make(map[string]ledger.Node, len(existing))
		for _, n := range existing {
			inLedger[n.Name] = n
		}
		inCluster := make(map[string]bool, len(snap.Nodes))
		for _, n := range snap.Nodes {
			inCluster[n.Name] = true
			if _, known := inLedger[n.Name]; !n.HasGPU && !known {
				continue
			}
			if _, err := q.UpsertNode(ctx, ledger.UpsertNodeParams{
				Name:         n.Name,
				Ready:        new(n.Ready),
				InstanceType: nonEmpty(n.InstanceType),
				LastSeenAt:   at,
			}); err != nil {
				return fmt.Errorf("upsert node %s: %w", n.Name, err)
			}
			st.Nodes++
		}
		for _, n := range existing {
			if inCluster[n.Name] || n.Ready == nil {
				continue
			}
			if _, err := q.DeleteNode(ctx, n.Name); err != nil {
				return fmt.Errorf("delete node %s: %w", n.Name, err)
			}
			c.logger.Info("node removed from cluster", "node", n.Name)
			st.DeletedNodes++
		}

		// 워크로드
		current, err := q.ListWorkloads(ctx)
		if err != nil {
			return fmt.Errorf("list workloads: %w", err)
		}
		type wkey struct{ kind, namespace, name string }
		keep := make(map[wkey]bool, len(snap.Workloads))
		for _, w := range snap.Workloads {
			p := ledger.UpsertWorkloadParams{
				Kind:       ledger.KindPod,
				Namespace:  w.Namespace,
				Name:       w.Name,
				UID:        nonEmpty(w.UID),
				NodeName:   nonEmpty(w.NodeName),
				Phase:      w.Phase,
				LastSeenAt: at,
			}
			if _, err := q.UpsertWorkload(ctx, p); err != nil {
				return fmt.Errorf("upsert pod %s/%s: %w", w.Namespace, w.Name, err)
			}
			keep[wkey{p.Kind, p.Namespace, p.Name}] = true
			st.Workloads++
		}
		for _, w := range current {
			if keep[wkey{w.Kind, w.Namespace, w.Name}] {
				continue
			}
			if _, err := q.DeleteWorkload(ctx, ledger.DeleteWorkloadParams{Kind: w.Kind, Namespace: w.Namespace, Name: w.Name}); err != nil {
				return fmt.Errorf("delete workload %s %s/%s: %w", w.Kind, w.Namespace, w.Name, err)
			}
			st.DeletedWorkloads++
		}

		return nil
	})
	if err != nil {
		return KubeStats{}, err
	}
	return st, nil
}
