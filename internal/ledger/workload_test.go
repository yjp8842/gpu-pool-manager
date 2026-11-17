package ledger

import (
	"testing"
	"time"
)

func TestWorkloads(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	pod, err := s.UpsertWorkload(ctx, UpsertWorkloadParams{
		Kind: KindPod, Namespace: "default", Name: "train-0", UID: new("uid-1"),
		NodeName: new("node-a"), Phase: "Running", LastSeenAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.UpsertWorkload(ctx, UpsertWorkloadParams{
		Kind: KindPod, Namespace: "default", Name: "train-0", UID: new("uid-1"),
		NodeName: new("node-a"), Phase: "Succeeded", LastSeenAt: t0.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != pod.ID || again.Phase != "Succeeded" {
		t.Fatalf("upsert same pod = id %d phase %s, want id %d phase Succeeded", again.ID, again.Phase, pod.ID)
	}

	// 노드가 정해지지 않은 Pending Pod도 넣을 수 있다 (node를 참조하지 않는다).
	if _, err := s.UpsertWorkload(ctx, UpsertWorkloadParams{
		Kind: KindPod, Namespace: "default", Name: "pending-0", Phase: "Pending", LastSeenAt: t0,
	}); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListWorkloads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("ListWorkloads returned %d, want 2", len(all))
	}

	n, err := s.DeleteWorkload(ctx, DeleteWorkloadParams{Kind: KindPod, Namespace: "default", Name: "train-0"})
	if err != nil || n != 1 {
		t.Fatalf("DeleteWorkload = %d, %v; want 1, nil", n, err)
	}
	_, err = s.GetWorkload(ctx, GetWorkloadParams{Kind: KindPod, Namespace: "default", Name: "train-0"})
	wantNotFound(t, err)
}

func TestWorkloadConstraints(t *testing.T) {
	s := newTestStore(t)
	_, err := s.UpsertWorkload(t.Context(), UpsertWorkloadParams{
		Kind: "JOB", Namespace: "default", Name: "w", Phase: "Running", LastSeenAt: t0,
	})
	wantViolation(t, err, IsCheckViolation, "workload_kind_check")
}
