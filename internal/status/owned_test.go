package status

import (
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func labelled(kind, ns, name, instance string) kube.Workload {
	w := kube.Workload{Kind: kind}
	w.Metadata.Namespace, w.Metadata.Name = ns, name
	w.Metadata.Labels = map[string]string{"app.kubernetes.io/instance": instance}
	return w
}

// An Application ArgoCD cannot introspect reports no resources, and the service then
// loses its version, its accelerator marking and any reason its pods are stuck. The
// workloads still carry the ownership label ArgoCD wrote.
func TestOwnedRecoveredFromLabelWhenApplicationReportsNothing(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "inference"}) // no Owned: sync status was Unknown

	FillOwnedFromLabels(snap, &kube.WorkloadList{Items: []kube.Workload{
		labelled("Deployment", "inference", "inference", "inference"),
		labelled("Deployment", "other", "other", "other"),
	}})

	got := snap.Services[0].Owned
	if len(got) != 1 || got[0].Name != "inference" || got[0].Namespace != "inference" {
		t.Fatalf("Owned=%+v, want the labelled workload", got)
	}
}

// A service that reported its own resources is believed. The label is also written by
// Helm, so overriding a good answer with it would be a downgrade.
func TestOwnedFromLabelDoesNotOverrideWhatArgoCDReported(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "api", Owned: []Component{
		{Kind: "Deployment", Namespace: "api", Name: "api-real"},
	}})

	FillOwnedFromLabels(snap, &kube.WorkloadList{Items: []kube.Workload{
		labelled("Deployment", "api", "api-imposter", "api"),
	}})

	if got := snap.Services[0].Owned; len(got) != 1 || got[0].Name != "api-real" {
		t.Fatalf("Owned=%+v, want what the Application reported", got)
	}
}

// A workload with no ownership label belongs to nobody here.
func TestOwnedFromLabelIgnoresUnlabelledWorkloads(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "api"})
	w := kube.Workload{Kind: "Deployment"}
	w.Metadata.Namespace, w.Metadata.Name = "api", "api"

	FillOwnedFromLabels(snap, &kube.WorkloadList{Items: []kube.Workload{w}})

	if len(snap.Services[0].Owned) != 0 {
		t.Fatal("an unlabelled workload must not be claimed")
	}
}

// Several workloads under one Application are all recovered.
func TestOwnedFromLabelCollectsEveryMatch(t *testing.T) {
	snap := &Snapshot{}
	snap.addService(Service{Name: "svc"})
	FillOwnedFromLabels(snap, &kube.WorkloadList{Items: []kube.Workload{
		labelled("Deployment", "ns", "a", "svc"),
		labelled("StatefulSet", "ns", "b", "svc"),
	}})
	if got := snap.Services[0].Owned; len(got) != 2 {
		t.Fatalf("Owned=%+v, want both", got)
	}
}
