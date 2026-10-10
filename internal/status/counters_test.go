package status

import (
	"encoding/json"
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func TestBuildPodStatsNilIsZeroValue(t *testing.T) {
	if got := BuildPodStats(nil); got != (PodStats{}) {
		t.Errorf("BuildPodStats(nil) = %+v, want zero value", got)
	}
}

func TestBuildPodStatsCountsReady(t *testing.T) {
	raw := `{"items":[
		{"status":{"conditions":[{"type":"Ready","status":"True"}]}},
		{"status":{"conditions":[{"type":"Ready","status":"False"}]}},
		{"status":{"conditions":[]}}
	]}`
	var list kube.PodList
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := BuildPodStats(&list)
	if got.Total != 3 {
		t.Errorf("Total = %d, want 3", got.Total)
	}
	if got.Ready != 1 {
		t.Errorf("Ready = %d, want 1", got.Ready)
	}
}

func TestBuildWorkloadStatsNilIsZeroValue(t *testing.T) {
	if got := BuildWorkloadStats(nil); got != (WorkloadStats{}) {
		t.Errorf("BuildWorkloadStats(nil) = %+v, want zero value", got)
	}
}

func TestBuildWorkloadStatsSumsAcrossKinds(t *testing.T) {
	list := kube.WorkloadList{Items: []kube.Workload{
		{Kind: kube.KindDeployment, Status: kube.WorkloadStatus{ReadyReplicas: 2, Replicas: 3}},
		{Kind: kube.KindDaemonSet, Status: kube.WorkloadStatus{NumberReady: 5, DesiredNumberScheduled: 5}},
	}}
	got := BuildWorkloadStats(&list)
	if got.Total != 2 {
		t.Errorf("Total = %d, want 2", got.Total)
	}
	if got.Ready != 7 || got.Desired != 8 {
		t.Errorf("Ready/Desired = %d/%d, want 7/8", got.Ready, got.Desired)
	}
}

func TestPodStatsPercentAndNotReady(t *testing.T) {
	s := PodStats{Total: 4, Ready: 3}
	if s.NotReady() != 1 {
		t.Errorf("NotReady() = %d, want 1", s.NotReady())
	}
	if s.Percent() != 75 {
		t.Errorf("Percent() = %d, want 75", s.Percent())
	}
	if (PodStats{}).Percent() != 0 {
		t.Error("Percent() on zero value should not divide by zero")
	}
}

func TestWorkloadStatsPercent(t *testing.T) {
	s := WorkloadStats{Total: 2, Ready: 7, Desired: 8}
	if s.Percent() != 87 && s.Percent() != 88 {
		t.Errorf("Percent() = %d, want ~87-88", s.Percent())
	}
	if (WorkloadStats{}).Percent() != 0 {
		t.Error("Percent() on zero value should not divide by zero")
	}
}
