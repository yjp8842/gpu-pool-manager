// Package collector는 서버1의 수집 작업이다.
//
// 회차마다 MetricSource에서 노드별 스냅샷을 받아 장부와 비교하고 관측 값만 반영한다.
// 할당 상태(allocation)는 바꾸지 않는다. 그것은 서버2의 정합 작업이 한다.
//   - 새로 보인 카드·조각은 넣고, 다시 보인 것은 갱신하며 미관측 횟수를 0으로 되돌린다.
//   - 스냅샷이 들어온 노드에서 보이지 않은 카드·조각은 미관측 횟수를 올리고, MissLimit번 연속이면 지운다.
//   - 이미 있던 GPU의 조각 구성이 바뀌면 MIG를 다시 나눈 것으로 보고 이력에 남긴다.
//
// 노드 하나는 한 트랜잭션으로 반영한다. 한 노드에서 실패해도 다른 노드는 반영한다.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/yjp8842/gpu-pool-manager/internal/history"
	"github.com/yjp8842/gpu-pool-manager/internal/ledger"
	"github.com/yjp8842/gpu-pool-manager/internal/metrics"
)

// DefaultMissLimit는 카드·조각을 지우기까지의 연속 미관측 횟수다.
const DefaultMissLimit = 3

// Collector는 MetricSource의 스냅샷을 장부에 반영한다.
type Collector struct {
	source    metrics.MetricSource
	store     *ledger.Store
	logger    *slog.Logger
	missLimit int
	rounds    int // Run이 돌린 회차 수. 첫 회차는 바뀐 것이 없어도 Info로 남긴다
}

// New는 Collector를 만든다.
func New(source metrics.MetricSource, store *ledger.Store, logger *slog.Logger) *Collector {
	return &Collector{source: source, store: store, logger: logger, missLimit: DefaultMissLimit}
}

// Stats는 한 회차의 반영 결과다.
type Stats struct {
	Nodes            int   // 반영한 노드 수
	GPUs             int   // 반영한 카드 수
	Instances        int   // 반영한 조각 수
	Skipped          int   // 부모 GPU가 스냅샷에 없어 건너뛴 조각 수
	MissedGPUs       int64 // 이번 회차에 보이지 않은 카드 수
	MissedInstances  int64 // 이번 회차에 보이지 않은 조각 수
	DeletedGPUs      int   // 연속 미관측으로 지운 카드 수
	DeletedInstances int   // 연속 미관측으로 지운 조각 수 (카드와 함께 지워진 것은 빼고)
	Reconfigured     int   // 조각 구성이 바뀐 GPU 수
}

func (s *Stats) add(o Stats) {
	s.Nodes += o.Nodes
	s.GPUs += o.GPUs
	s.Instances += o.Instances
	s.Skipped += o.Skipped
	s.MissedGPUs += o.MissedGPUs
	s.MissedInstances += o.MissedInstances
	s.DeletedGPUs += o.DeletedGPUs
	s.DeletedInstances += o.DeletedInstances
	s.Reconfigured += o.Reconfigured
}

// changed는 이번 회차에 장부의 구성이 바뀌었는지 알려준다 (로그 수준을 정할 때 쓴다).
func (s Stats) changed() bool {
	return s.Skipped > 0 || s.MissedGPUs > 0 || s.MissedInstances > 0 ||
		s.DeletedGPUs > 0 || s.DeletedInstances > 0 || s.Reconfigured > 0
}

// Run은 interval마다 한 회차씩 수집한다. ctx가 끝나면 돌아온다.
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		c.runAndLog(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Collector) runAndLog(ctx context.Context) {
	start := time.Now()
	st, err := c.RunOnce(ctx)
	if ctx.Err() != nil {
		return
	}
	c.rounds++
	attrs := []any{
		"nodes", st.Nodes, "gpus", st.GPUs, "instances", st.Instances,
		"missed_gpus", st.MissedGPUs, "missed_instances", st.MissedInstances,
		"deleted_gpus", st.DeletedGPUs, "deleted_instances", st.DeletedInstances,
		"reconfigured", st.Reconfigured, "skipped", st.Skipped,
		"took", time.Since(start).String(),
	}
	switch {
	case err != nil:
		c.logger.Warn("collect round had errors", append(attrs, "err", err)...)
	case st.changed() || c.rounds == 1:
		c.logger.Info("collect round", attrs...)
	default:
		c.logger.Debug("collect round", attrs...)
	}
}

// RunOnce는 한 회차를 수집해 장부에 반영한다. 일부 노드를 읽지 못하거나 반영하지 못해도
// 나머지 노드는 반영하고, 그 오류들을 묶어 돌려준다.
func (c *Collector) RunOnce(ctx context.Context) (Stats, error) {
	snaps, fetchErr := c.source.Fetch(ctx)
	var (
		total Stats
		errs  []error
	)
	if fetchErr != nil {
		errs = append(errs, fmt.Errorf("fetch: %w", fetchErr))
	}
	for _, snap := range snaps {
		st, err := c.syncNode(ctx, snap)
		if err != nil {
			errs = append(errs, fmt.Errorf("node %q: %w", snap.Node, err))
			continue
		}
		total.add(st)
	}
	return total, errors.Join(errs...)
}

// syncNode는 노드 하나의 스냅샷을 한 트랜잭션으로 반영한다.
func (c *Collector) syncNode(ctx context.Context, snap metrics.Snapshot) (Stats, error) {
	if snap.Node == "" {
		return Stats{}, errors.New("snapshot has no node name")
	}
	var st Stats
	err := c.store.InTx(ctx, func(q *ledger.Queries) error {
		st = Stats{Nodes: 1}
		if err := q.EnsureNode(ctx, ledger.EnsureNodeParams{Name: snap.Node, LastSeenAt: snap.At}); err != nil {
			return fmt.Errorf("ensure node: %w", err)
		}

		// 반영하기 전에 GPU별 지난 구성을 읽어 둔다. 재분할을 알아보는 기준이다.
		before, err := layoutsBefore(ctx, q, snap.Node)
		if err != nil {
			return err
		}

		// 카드
		gpus := make(map[string]metrics.GPU, len(snap.GPUs))
		seenGPUs := make([]string, 0, len(snap.GPUs)) // NULL이 아닌 빈 배열이어야 한다
		for _, g := range snap.GPUs {
			if _, err := q.UpsertGPUDevice(ctx, deviceParams(snap, g)); err != nil {
				return fmt.Errorf("upsert gpu %s: %w", g.UUID, err)
			}
			gpus[g.UUID] = g
			seenGPUs = append(seenGPUs, g.UUID)
		}
		st.GPUs = len(gpus)

		// 조각
		after := make(map[string]layout, len(gpus))
		for uuid := range gpus {
			after[uuid] = layout{}
		}
		seenIDs := make([]int64, 0, len(snap.Instances))
		for _, in := range snap.Instances {
			if _, ok := gpus[in.ParentUUID]; !ok {
				st.Skipped++
				c.logger.Warn("instance without its GPU in snapshot",
					"node", snap.Node, "gpu", in.ParentUUID, "gi_id", in.GIID, "profile", in.Profile)
				continue
			}
			row, err := q.UpsertGPUInstance(ctx, instanceParams(snap, in))
			if err != nil {
				return fmt.Errorf("upsert instance %s/%d/%s: %w", in.ParentUUID, in.GIID, in.Profile, err)
			}
			seenIDs = append(seenIDs, row.ID)
			after[in.ParentUUID].add(in.Profile)
			st.Instances++
		}

		// 재분할: 이미 있던 GPU의 구성이 지난 회차와 다르면 이력에 남긴다. 처음 보인 GPU는 남기지 않는다.
		for uuid, prev := range before {
			now, ok := after[uuid]
			if !ok || prev.equal(now) {
				continue
			}
			if err := history.MIGReconfigured(ctx, q, snap.At, snap.Node, uuid, prev.String(), now.String()); err != nil {
				return err
			}
			st.Reconfigured++
		}

		// 미관측: 조각을 먼저 처리한다. 카드가 지워지면 그 카드의 조각도 함께 지워진다.
		if st.MissedInstances, err = q.MarkMissingGPUInstances(ctx, ledger.MarkMissingGPUInstancesParams{
			NodeName: snap.Node, SeenIDs: seenIDs,
		}); err != nil {
			return fmt.Errorf("mark missing instances: %w", err)
		}
		deletedIns, err := q.DeleteMissingGPUInstances(ctx, ledger.DeleteMissingGPUInstancesParams{
			NodeName: snap.Node, MissLimit: int32(c.missLimit),
		})
		if err != nil {
			return fmt.Errorf("delete missing instances: %w", err)
		}
		st.DeletedInstances = len(deletedIns)
		for _, d := range deletedIns {
			c.logger.Info("instance removed after missed rounds",
				"node", snap.Node, "gpu", d.ParentUUID, "gi_id", d.GIID, "profile", d.Profile, "missed", c.missLimit)
		}

		if st.MissedGPUs, err = q.MarkMissingGPUDevices(ctx, ledger.MarkMissingGPUDevicesParams{
			NodeName: snap.Node, SeenUUIDs: seenGPUs,
		}); err != nil {
			return fmt.Errorf("mark missing gpus: %w", err)
		}
		deletedGPUs, err := q.DeleteMissingGPUDevices(ctx, ledger.DeleteMissingGPUDevicesParams{
			NodeName: snap.Node, MissLimit: int32(c.missLimit),
		})
		if err != nil {
			return fmt.Errorf("delete missing gpus: %w", err)
		}
		st.DeletedGPUs = len(deletedGPUs)
		for _, uuid := range deletedGPUs {
			c.logger.Info("gpu removed after missed rounds", "node", snap.Node, "gpu", uuid, "missed", c.missLimit)
		}
		return nil
	})
	if err != nil {
		return Stats{}, err
	}
	return st, nil
}

// layoutsBefore는 노드의 GPU별 지난 구성을 돌려준다. 지난 구성은 그 GPU가 마지막으로 보인 회차에
// 함께 보인 조각들이다. 카드와 조각의 미관측 횟수는 함께 오르므로, 횟수가 카드와 같은 조각이 그 회차의 조각이다.
// 그래서 카드가 몇 회차 안 보였다가 다시 보여도 구성이 바뀐 것으로 보지 않는다.
func layoutsBefore(ctx context.Context, q *ledger.Queries, node string) (map[string]layout, error) {
	devices, err := q.ListGPUDevices(ctx, &node)
	if err != nil {
		return nil, fmt.Errorf("list gpus: %w", err)
	}
	instances, err := q.ListGPUInstances(ctx, ledger.ListGPUInstancesParams{NodeName: &node})
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	missed := make(map[string]int32, len(devices))
	out := make(map[string]layout, len(devices))
	for _, d := range devices {
		missed[d.UUID] = d.MissedCount
		out[d.UUID] = layout{}
	}
	for _, r := range instances {
		in := r.GPUInstance
		if m, ok := missed[in.ParentUUID]; ok && in.MissedCount == m {
			out[in.ParentUUID].add(in.Profile)
		}
	}
	return out, nil
}

func deviceParams(snap metrics.Snapshot, g metrics.GPU) ledger.UpsertGPUDeviceParams {
	p := ledger.UpsertGPUDeviceParams{
		UUID:        g.UUID,
		NodeName:    snap.Node,
		GPUIndex:    int32(g.Index),
		Model:       g.Model,
		MemoryMB:    positive(g.MemoryMB),
		MIGEnabled:  g.MIGEnabled,
		TotalSlices: positive(g.TotalSlices),
		LastSeenAt:  snap.At,
	}
	if xid, ok := snap.LastXID[g.UUID]; ok {
		p.LastXID = new(int32(xid))
	}
	return p
}

func instanceParams(snap metrics.Snapshot, in metrics.Instance) ledger.UpsertGPUInstanceParams {
	p := ledger.UpsertGPUInstanceParams{
		ParentUUID: in.ParentUUID,
		GIID:       int32(in.GIID),
		Profile:    in.Profile,
		MemoryMB:   positive(in.MemoryMB),
		MIGUUID:    nonEmpty(in.MIGUUID),
		Health:     healthFromXID(snap.LastXID[in.ParentUUID]),
		LastSeenAt: snap.At,
	}
	if pod := in.Pod; pod != nil {
		p.ObservedPodNamespace = &pod.Namespace
		p.ObservedPodName = &pod.Name
		p.ObservedContainer = nonEmpty(pod.Container)
	}
	return p
}

// criticalXIDs는 GPU 리셋이 필요한 하드웨어 계열 XID다. 이 값이면 그 GPU의 조각을 비정상으로 본다.
// 애플리케이션 오류로도 나는 XID(13, 31, 43 등)는 넣지 않는다.
var criticalXIDs = map[int]bool{
	48:  true, // DBE (double bit ECC) 오류
	63:  true, // ECC 페이지 퇴역·행 재매핑 기록
	64:  true, // ECC 페이지 퇴역·행 재매핑 실패
	74:  true, // NVLink 오류
	79:  true, // GPU가 버스에서 떨어짐
	92:  true, // 높은 단일 비트 ECC 오류율
	95:  true, // 격리되지 않은 ECC 오류
	119: true, // GSP RPC 시간 초과
	120: true, // GSP 오류
}

// healthFromXID는 GPU의 마지막 XID로 조각의 건강 상태를 정한다. XID 메트릭이 없으면(0) 오류가 없는 것으로 본다.
func healthFromXID(xid int) string {
	if criticalXIDs[xid] {
		return ledger.HealthUnhealthy
	}
	return ledger.HealthOK
}

func positive(v int) *int32 {
	if v <= 0 {
		return nil
	}
	return new(int32(v))
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
