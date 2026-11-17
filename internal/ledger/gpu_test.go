package ledger

import (
	"testing"
	"time"
)

func TestNodeUpsertAndEnsure(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	// DCGM에서 먼저 보이면 이름만 들어간다.
	seedNode(t, s, "node-a")
	n, err := s.GetNode(ctx, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if n.Ready != nil || n.InstanceType != nil {
		t.Fatalf("ensured node = ready %v instance type %v, want nil", n.Ready, n.InstanceType)
	}

	// K8s에서 읽으면 값이 채워진다.
	n, err = s.UpsertNode(ctx, UpsertNodeParams{
		Name:         "node-a",
		Ready:        new(true),
		InstanceType: new("p6-b200.48xlarge"),
		LastSeenAt:   t0.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.Ready == nil || !*n.Ready || *n.InstanceType != "p6-b200.48xlarge" {
		t.Fatalf("upserted node = %+v", n)
	}

	// 이미 있는 노드에 EnsureNode를 다시 불러도 K8s 값은 그대로다.
	seedNode(t, s, "node-a")
	n, err = s.GetNode(ctx, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if n.Ready == nil || !*n.Ready || !n.LastSeenAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("EnsureNode overwrote node: %+v", n)
	}

	if _, err := s.GetNode(ctx, "missing"); err != nil {
		wantNotFound(t, err)
	} else {
		t.Fatal("GetNode(missing) succeeded")
	}
}

func TestGPUDeviceAndInstances(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	seedNode(t, s, "node-a")
	seedNode(t, s, "node-b")
	seedDevice(t, s, "node-a", "GPU-a0", 0)
	seedDevice(t, s, "node-a", "GPU-a1", 1)
	seedDevice(t, s, "node-b", "GPU-b0", 0)

	// GPU-a0은 1g.23gb 세 조각, GPU-a1과 GPU-b0은 나누지 않은 GPU.
	for _, gi := range []int32{9, 7, 8} {
		seedInstance(t, s, "GPU-a0", gi, "1g.23gb", HealthOK)
	}
	seedInstance(t, s, "GPU-a1", 0, "full", HealthOK)
	seedInstance(t, s, "GPU-b0", 0, "full", HealthOK)

	devices, err := s.ListGPUDevices(ctx, new("node-a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices[0].UUID != "GPU-a0" || devices[1].UUID != "GPU-a1" {
		t.Fatalf("ListGPUDevices(node-a) = %+v", devices)
	}
	all, err := s.ListGPUDevices(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("ListGPUDevices(nil) returned %d devices, want 3", len(all))
	}

	// 조각의 노드는 gpu_device와 조인해서 나온다. 카드 번호, GI ID 순서다.
	rows, err := s.ListGPUInstances(ctx, ListGPUInstancesParams{NodeName: new("node-a")})
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		node    string
		index   int32
		gi      int32
		profile string
	}
	want := []key{
		{"node-a", 0, 7, "1g.23gb"},
		{"node-a", 0, 8, "1g.23gb"},
		{"node-a", 0, 9, "1g.23gb"},
		{"node-a", 1, 0, "full"},
	}
	if len(rows) != len(want) {
		t.Fatalf("ListGPUInstances(node-a) returned %d rows, want %d", len(rows), len(want))
	}
	for i, r := range rows {
		got := key{r.NodeName, r.GPUIndex, r.GPUInstance.GIID, r.GPUInstance.Profile}
		if got != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got, want[i])
		}
	}

	rows, err = s.ListGPUInstances(ctx, ListGPUInstancesParams{Profile: new("full")})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("ListGPUInstances(profile full) returned %d rows, want 2", len(rows))
	}
}

func TestGPUInstanceUpsertKeepsKey(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	seedNode(t, s, "node-a")
	seedDevice(t, s, "node-a", "GPU-a0", 0)
	first := seedInstance(t, s, "GPU-a0", 7, "1g.23gb", HealthOK)

	// 미관측 횟수가 쌓인 뒤 다시 관측되면 0으로 돌아간다.
	if _, err := s.pool.Exec(ctx, "UPDATE gpu_instance SET missed_count = 2 WHERE id = $1", first.ID); err != nil {
		t.Fatal(err)
	}

	// 같은 고정 키면 MIG UUID가 바뀌어도 같은 행이다.
	again, err := s.UpsertGPUInstance(ctx, UpsertGPUInstanceParams{
		ParentUUID:           "GPU-a0",
		GIID:                 7,
		Profile:              "1g.23gb",
		MIGUUID:              new("MIG-new"),
		Health:               HealthOK,
		ObservedPodNamespace: new("default"),
		ObservedPodName:      new("train-0"),
		LastSeenAt:           t0.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("same key got new id %d, want %d", again.ID, first.ID)
	}
	if *again.MIGUUID != "MIG-new" || *again.ObservedPodName != "train-0" || again.MissedCount != 0 {
		t.Fatalf("upserted instance = %+v", again)
	}

	// 같은 GI ID라도 프로파일이 다르면 다른 조각이다 (재분할).
	other := seedInstance(t, s, "GPU-a0", 7, "2g.45gb", HealthOK)
	if other.ID == first.ID {
		t.Fatal("different profile reused the same row")
	}
}

func TestGPUConstraints(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedNode(t, s, "node-a")
	seedDevice(t, s, "node-a", "GPU-a0", 0)

	instance := func(gi int32, profile, health string, podNS, podName *string) error {
		_, err := s.UpsertGPUInstance(ctx, UpsertGPUInstanceParams{
			ParentUUID:           "GPU-a0",
			GIID:                 gi,
			Profile:              profile,
			Health:               health,
			ObservedPodNamespace: podNS,
			ObservedPodName:      podName,
			LastSeenAt:           t0,
		})
		return err
	}

	t.Run("full GPU has GI ID 0", func(t *testing.T) {
		err := instance(1, "full", HealthOK, nil, nil)
		wantViolation(t, err, IsCheckViolation, "gpu_instance_full_gi_id_check")
	})
	t.Run("health value", func(t *testing.T) {
		err := instance(7, "1g.23gb", "BROKEN", nil, nil)
		wantViolation(t, err, IsCheckViolation, "gpu_instance_health_check")
	})
	t.Run("observed pod needs namespace and name", func(t *testing.T) {
		err := instance(7, "1g.23gb", HealthOK, new("default"), nil)
		wantViolation(t, err, IsCheckViolation, "gpu_instance_observed_pod_check")
	})
	t.Run("instance needs its GPU", func(t *testing.T) {
		_, err := s.UpsertGPUInstance(ctx, UpsertGPUInstanceParams{
			ParentUUID: "GPU-missing", Profile: "full", Health: HealthOK, LastSeenAt: t0,
		})
		wantViolation(t, err, IsForeignKeyViolation, "gpu_instance_parent_uuid_fkey")
	})
	t.Run("GPU needs its node", func(t *testing.T) {
		_, err := s.UpsertGPUDevice(ctx, UpsertGPUDeviceParams{
			UUID: "GPU-x", NodeName: "node-missing", Model: "NVIDIA B200", LastSeenAt: t0,
		})
		wantViolation(t, err, IsForeignKeyViolation, "gpu_device_node_name_fkey")
	})
}

func TestDeleteNodeCascades(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	seedNode(t, s, "node-a")
	seedDevice(t, s, "node-a", "GPU-a0", 0)
	in := seedInstance(t, s, "GPU-a0", 7, "1g.23gb", HealthOK)
	a := observe(t, s, "node-a", "1g.23gb", in.ID, "train-0")

	n, err := s.DeleteNode(ctx, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("DeleteNode removed %d rows, want 1", n)
	}

	_, err = s.GetGPUDevice(ctx, "GPU-a0")
	wantNotFound(t, err)
	_, err = s.GetGPUInstance(ctx, in.ID)
	wantNotFound(t, err)

	// 할당 행은 남고 조각 참조만 NULL이 된다. 정합 작업이 이 할당을 닫는다.
	got, err := s.GetAllocation(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InstanceID != nil || got.State != StateAllocated {
		t.Fatalf("allocation after node delete = instance %v state %s, want nil and ALLOCATED", got.InstanceID, got.State)
	}
}
