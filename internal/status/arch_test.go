package status

import (
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func archNode(name, arch string, labels map[string]string) kube.Node {
	var n kube.Node
	n.Metadata.Name, n.Metadata.Labels = name, labels
	n.Status.NodeInfo.Architecture = arch
	n.Status.Capacity = map[string]kube.Quantity{"cpu": "4"}
	return n
}

func archSvc(name, ns, wl string) Service {
	return Service{Name: name, Owned: []Component{{Kind: "Deployment", Namespace: ns, Name: wl}}}
}

func TestFillArch(t *testing.T) {
	nodes := &kube.NodeList{Items: []kube.Node{
		archNode("g1", "arm64", map[string]string{"pool": "graviton"}),
		archNode("g2", "arm64", map[string]string{"pool": "graviton"}),
		archNode("x1", "amd64", map[string]string{"pool": "intel"}),
	}}

	cases := []struct {
		name     string
		pool     string // "" means unconstrained
		wantArch string
	}{
		{name: "pinned to arm64 nodes", pool: "graviton", wantArch: "arm64"},
		{name: "pinned to amd64 nodes", pool: "intel", wantArch: "amd64"},
		// Free to land anywhere, so claiming an architecture would be a guess.
		{name: "unconstrained gets no badge", pool: "", wantArch: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := &Snapshot{}
			snap.addService(archSvc("svc", "ml", "svc"))
			w := kube.Workload{Kind: "Deployment"}
			w.Metadata.Namespace, w.Metadata.Name = "ml", "svc"
			if tc.pool != "" {
				w = affinityTo(w, "pool", tc.pool)
			}
			FillArch(snap, &kube.WorkloadList{Items: []kube.Workload{w}}, nodes)

			if got := snap.Services[0].Arch; got != tc.wantArch {
				t.Fatalf("Arch=%q, want %q", got, tc.wantArch)
			}
		})
	}
}

// A service owning workloads on different architectures is not pinned to either.
func TestFillArchMixedOwnedWorkloadsGetNoBadge(t *testing.T) {
	nodes := &kube.NodeList{Items: []kube.Node{
		archNode("g1", "arm64", map[string]string{"pool": "graviton"}),
		archNode("x1", "amd64", map[string]string{"pool": "intel"}),
	}}
	snap := &Snapshot{}
	snap.addService(Service{Name: "multi", Owned: []Component{
		{Kind: "Deployment", Namespace: "ml", Name: "a"},
		{Kind: "Deployment", Namespace: "ml", Name: "b"},
	}})
	mk := func(n, pool string) kube.Workload {
		w := kube.Workload{Kind: "Deployment"}
		w.Metadata.Namespace, w.Metadata.Name = "ml", n
		return affinityTo(w, "pool", pool)
	}
	FillArch(snap, &kube.WorkloadList{Items: []kube.Workload{mk("a", "graviton"), mk("b", "intel")}}, nodes)

	if got := snap.Services[0].Arch; got != "" {
		t.Fatalf("Arch=%q, want empty for a service spanning both", got)
	}
}

func TestFillArchCountsAndIsIdempotent(t *testing.T) {
	nodes := &kube.NodeList{Items: []kube.Node{
		archNode("g1", "arm64", map[string]string{"pool": "graviton"}),
		archNode("x1", "amd64", map[string]string{"pool": "intel"}),
	}}
	snap := &Snapshot{}
	snap.addService(archSvc("a", "ml", "a"))
	snap.addService(archSvc("b", "ml", "b"))
	mk := func(n, pool string) kube.Workload {
		w := kube.Workload{Kind: "Deployment"}
		w.Metadata.Namespace, w.Metadata.Name = "ml", n
		return affinityTo(w, "pool", pool)
	}
	list := &kube.WorkloadList{Items: []kube.Workload{mk("a", "graviton"), mk("b", "graviton")}}

	for i := 0; i < 3; i++ {
		FillArch(snap, list, nodes)
	}
	if len(snap.ArchCounts) != 1 || snap.ArchCounts[0].Arch != "arm64" || snap.ArchCounts[0].Count != 2 {
		t.Fatalf("ArchCounts=%+v, want one arm64 entry counting 2", snap.ArchCounts)
	}
}
