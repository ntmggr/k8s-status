package status

import (
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func TestFillReadinessSumsAcrossOwnedWorkloads(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "billing", Owned: []Component{
		{Kind: "Deployment", Name: "billing-api", Namespace: "billing"},
		{Kind: "Deployment", Name: "billing-worker", Namespace: "billing"},
		{Kind: "Job", Name: "billing-migrate", Namespace: "billing"}, // ignored, not a workload kind
	}})

	workloads := &kube.WorkloadList{Items: []kube.Workload{
		{Kind: kube.KindDeployment, Metadata: kube.WorkloadMetadata{Name: "billing-api", Namespace: "billing"},
			Status: kube.WorkloadStatus{ReadyReplicas: 2, Replicas: 3}},
		{Kind: kube.KindDeployment, Metadata: kube.WorkloadMetadata{Name: "billing-worker", Namespace: "billing"},
			Status: kube.WorkloadStatus{ReadyReplicas: 1, Replicas: 1}},
		{Kind: kube.KindDeployment, Metadata: kube.WorkloadMetadata{Name: "unrelated", Namespace: "other"},
			Status: kube.WorkloadStatus{ReadyReplicas: 5, Replicas: 5}},
	}}

	FillReadiness(snap, workloads)

	got := snap.Services[0].PodsReady
	if !got.Found || got.Ready != 3 || got.Desired != 4 {
		t.Errorf("PodsReady = %+v, want Found=true Ready=3 Desired=4 (2+1 ready, 3+1 desired)", got)
	}
}

func TestFillReadinessDaemonSetUsesItsOwnCounters(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "logging", Owned: []Component{
		{Kind: "DaemonSet", Name: "log-agent", Namespace: "logging"},
	}})
	workloads := &kube.WorkloadList{Items: []kube.Workload{
		{Kind: kube.KindDaemonSet, Metadata: kube.WorkloadMetadata{Name: "log-agent", Namespace: "logging"},
			Status: kube.WorkloadStatus{NumberReady: 81, DesiredNumberScheduled: 82}},
	}}

	FillReadiness(snap, workloads)

	got := snap.Services[0].PodsReady
	if !got.Found || got.Ready != 81 || got.Desired != 82 {
		t.Errorf("PodsReady = %+v, want Found=true Ready=81 Desired=82", got)
	}
}

func TestFillReadinessNotFoundWhenNilOrUnmatched(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "billing", Owned: []Component{
		{Kind: "Deployment", Name: "gone-now", Namespace: "billing"},
	}})

	FillReadiness(snap, nil)
	if got := snap.Services[0].PodsReady; got.Found {
		t.Errorf("PodsReady = %+v, want Found=false with a nil workload list", got)
	}

	FillReadiness(snap, &kube.WorkloadList{})
	if got := snap.Services[0].PodsReady; got.Found {
		t.Errorf("PodsReady = %+v, want Found=false when the owned workload no longer exists", got)
	}
}

func TestFillReadinessResetsOnRepeatedCalls(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "billing", Owned: []Component{
		{Kind: "Deployment", Name: "billing-api", Namespace: "billing"},
	}})
	workloads := &kube.WorkloadList{Items: []kube.Workload{
		{Kind: kube.KindDeployment, Metadata: kube.WorkloadMetadata{Name: "billing-api", Namespace: "billing"},
			Status: kube.WorkloadStatus{ReadyReplicas: 1, Replicas: 1}},
	}}

	for i := 0; i < 3; i++ {
		FillReadiness(snap, workloads)
	}
	if got := snap.Services[0].PodsReady; got.Ready != 1 || got.Desired != 1 {
		t.Errorf("PodsReady after 3 calls = %+v, want Ready=1 Desired=1 (must not accumulate)", got)
	}
}
