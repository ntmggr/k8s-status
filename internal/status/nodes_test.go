package status

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// buildStats mirrors production, where the accelerator list comes from discovery.
func buildStats(list *kube.NodeList) NodeStats {
	return BuildNodeStats(list, DiscoverAccelerators(list, nil))
}

func decodeNodes(t *testing.T, raw string) *kube.NodeList {
	t.Helper()
	var list kube.NodeList
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("decode node list: %v", err)
	}
	return &list
}

const nodeFixture = `{"items":[
 {"metadata":{"name":"gpu-a"},"status":{"capacity":{"cpu":"8","nvidia.com/gpu":"4"},"nodeInfo":{"architecture":"amd64"}}},
 {"metadata":{"name":"gpu-b"},"status":{"capacity":{"nvidia.com/gpu":"1"},"nodeInfo":{"architecture":"amd64"}}},
 {"metadata":{"name":"cpu-a"},"status":{"capacity":{"cpu":"4"},"nodeInfo":{"architecture":"arm64"}}},
 {"metadata":{"name":"cpu-b"},"status":{"nodeInfo":{"architecture":"arm64"}}},
 {"metadata":{"name":"cpu-c"},"status":{"capacity":{"nvidia.com/gpu":"0"},"nodeInfo":{"architecture":"arm64"}}},
 {"metadata":{"name":"cpu-d"},"status":{"capacity":{"nvidia.com/gpu":"not-a-number"},"nodeInfo":{"architecture":"arm64"}}},
 {"metadata":{"name":"cpu-e"},"status":{"capacity":{"nvidia.com/gpu":null},"nodeInfo":{}}}
]}`

func TestBuildNodeStatsClassifiesAndSums(t *testing.T) {
	got := buildStats(decodeNodes(t, nodeFixture))

	if got.Total != 7 {
		t.Errorf("total = %d, want 7", got.Total)
	}
	if got.GPUNodes != 2 {
		t.Errorf("gpu nodes = %d, want 2", got.GPUNodes)
	}
	if got.CPUNodes != 5 {
		t.Errorf("cpu nodes = %d, want 5", got.CPUNodes)
	}
	if got.GPUs != 5 {
		t.Errorf("gpus = %d, want 5", got.GPUs)
	}
	if got.CPUNodes+got.GPUNodes != got.Total {
		t.Errorf("cpu+gpu = %d, want %d", got.CPUNodes+got.GPUNodes, got.Total)
	}
}

func TestBuildNodeStatsArchTally(t *testing.T) {
	got := buildStats(decodeNodes(t, nodeFixture))

	want := []ArchCount{{Arch: "arm64", Count: 4}, {Arch: "amd64", Count: 2}, {Arch: archUnknown, Count: 1}}
	if len(got.Arch) != len(want) {
		t.Fatalf("arch = %+v, want %+v", got.Arch, want)
	}
	for i := range want {
		if got.Arch[i] != want[i] {
			t.Errorf("arch[%d] = %+v, want %+v", i, got.Arch[i], want[i])
		}
	}
}

func TestBuildNodeStatsZoneTally(t *testing.T) {
	got := buildStats(decodeNodes(t, `{"items":[
	 {"metadata":{"name":"a","labels":{"topology.kubernetes.io/zone":"eu-west-1a"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}},
	 {"metadata":{"name":"b","labels":{"topology.kubernetes.io/zone":"eu-west-1a"}},"status":{"conditions":[{"type":"Ready","status":"False"}]}},
	 {"metadata":{"name":"c","labels":{"failure-domain.beta.kubernetes.io/zone":"eu-west-1b"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}},
	 {"metadata":{"name":"d"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}
	]}`))

	want := []ZoneCount{
		{Zone: "eu-west-1a", Nodes: 2, Ready: 1},
		{Zone: "eu-west-1b", Nodes: 1, Ready: 1},
		{Zone: zoneUnknown, Nodes: 1, Ready: 1},
	}
	if len(got.Zones) != len(want) {
		t.Fatalf("zones = %+v, want %+v", got.Zones, want)
	}
	for i := range want {
		if got.Zones[i] != want[i] {
			t.Errorf("zones[%d] = %+v, want %+v", i, got.Zones[i], want[i])
		}
	}
}

func TestBuildNodeStatsAcceptsBareNumberQuantity(t *testing.T) {
	got := buildStats(decodeNodes(t,
		`{"items":[{"metadata":{"name":"g"},"status":{"capacity":{"nvidia.com/gpu":2},"nodeInfo":{"architecture":"amd64"}}}]}`))

	if got.GPUNodes != 1 || got.GPUs != 2 {
		t.Errorf("gpuNodes = %d, gpus = %d, want 1 and 2", got.GPUNodes, got.GPUs)
	}
}

func TestBuildNodeStatsEmptyAndNil(t *testing.T) {
	if got := buildStats(nil); got.Total != 0 || len(got.Arch) != 0 {
		t.Errorf("nil list = %+v", got)
	}
	if got := buildStats(&kube.NodeList{}); got.Total != 0 || len(got.Arch) != 0 {
		t.Errorf("empty list = %+v", got)
	}
}

type fakeNodeLister struct {
	called int
	list   *kube.NodeList
	err    error
}

func (f *fakeNodeLister) ListNodes(context.Context) (*kube.NodeList, error) {
	f.called++
	return f.list, f.err
}

// The default deployment holds no ClusterRole, so a collector without WithNodes must
// never reach for the cluster-scoped nodes API.
func TestNodesNotFetchedWhenDisabled(t *testing.T) {
	nodeLister := &fakeNodeLister{list: decodeNodes(t, nodeFixture)}
	c, _ := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snap.Nodes != nil {
		t.Errorf("Nodes = %+v, want nil when the feature is off", snap.Nodes)
	}
	if nodeLister.called != 0 {
		t.Errorf("nodes API called %d times, want 0", nodeLister.called)
	}
}

func TestNodesFetchedOncePerTTL(t *testing.T) {
	nodeLister := &fakeNodeLister{list: decodeNodes(t, nodeFixture)}
	c, clock := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithNodes(nodeLister)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snap.Nodes == nil || snap.Nodes.Total != 7 {
		t.Fatalf("Nodes = %+v", snap.Nodes)
	}

	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("cached get: %v", err)
	}
	if nodeLister.called != 1 {
		t.Errorf("nodes fetches within TTL = %d, want 1", nodeLister.called)
	}

	clock.advance(16 * time.Second)
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("refresh get: %v", err)
	}
	if nodeLister.called != 2 {
		t.Errorf("nodes fetches after TTL = %d, want 2", nodeLister.called)
	}
}

func TestNodesDeniedDegradesInsteadOfFailing(t *testing.T) {
	nodeLister := &fakeNodeLister{err: &kube.StatusError{Code: http.StatusForbidden, Body: `nodes is forbidden`}}
	c, _ := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithNodes(nodeLister)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("a denied nodes read must not fail the snapshot: %v", err)
	}
	if snap.Summary.Total != 14 {
		t.Errorf("application data lost: total = %d, want 14", snap.Summary.Total)
	}
	if snap.Nodes == nil {
		t.Fatal("want a NodeStats carrying the error")
	}
	if !snap.Nodes.Denied {
		t.Error("403 should be reported as denied")
	}
	if snap.Nodes.Error == "" {
		t.Error("want the error text surfaced in the section")
	}
}

func TestNodesTransportErrorIsNotDenied(t *testing.T) {
	nodeLister := &fakeNodeLister{err: errors.New("query kubernetes api: connection refused")}
	c, _ := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithNodes(nodeLister)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snap.Nodes.Denied {
		t.Error("a transport error is not an RBAC denial")
	}
}

func TestParseKubernetesVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"v1.32.3-eks-be96eb4", "1.32"},
		{"v1.29.0-gke.1234000", "1.29"},
		{"v1.31.2", "1.31"},
		{"1.31.2", "1.31"},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseKubernetesVersion(c.in); got != c.want {
			t.Errorf("parseKubernetesVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDetectProvider(t *testing.T) {
	cases := []struct {
		name, providerID, kubeletVersion, want string
	}{
		{"eks", "aws:///us-east-1a/i-0123456789abcdef0", "v1.32.3-eks-be96eb4", "AWS EKS"},
		{"self-managed aws", "aws:///us-east-1a/i-0123456789abcdef0", "v1.32.3", "AWS"},
		{"aks", "azure:///subscriptions/x/resourceGroups/MC_myrg_mycluster_eastus/providers/Microsoft.Compute/virtualMachineScaleSets/aks-nodepool1/virtualMachines/0", "v1.29.0", "Azure AKS"},
		{"self-managed azure", "azure:///subscriptions/x/resourceGroups/myrg/providers/Microsoft.Compute/virtualMachines/vm1", "v1.29.0", "Azure"},
		{"gke", "gce://my-project/us-central1-a/gke-node-1", "v1.29.0-gke.1234000", "Google GKE"},
		{"self-managed gce", "gce://my-project/us-central1-a/vm-1", "v1.29.0", "Google Cloud"},
		{"unrecognized", "", "v1.31.2", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectProvider(c.providerID, c.kubeletVersion); got != c.want {
				t.Errorf("detectProvider(%q, %q) = %q, want %q", c.providerID, c.kubeletVersion, got, c.want)
			}
		})
	}
}

// TestBuildNodeStatsPrefersReadyNodeForVersion covers a cluster mid-upgrade: a
// NotReady node can be stale (shut down, or left over from a previous version), so
// its kubelet version and ProviderID must not win over a Ready node's when one is
// available in the same list.
func TestBuildNodeStatsPrefersReadyNodeForVersion(t *testing.T) {
	fixture := `{"items":[
 {"metadata":{"name":"stale"},"spec":{"providerID":"aws:///us-east-1a/i-old"},"status":{"conditions":[{"type":"Ready","status":"False"}],"nodeInfo":{"kubeletVersion":"v1.31.0-eks-abc"}}},
 {"metadata":{"name":"current"},"spec":{"providerID":"aws:///us-east-1a/i-new"},"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"kubeletVersion":"v1.32.3-eks-be96eb4"}}}
]}`
	got := buildStats(decodeNodes(t, fixture))
	if got.KubernetesVersion != "1.32" {
		t.Errorf("KubernetesVersion = %q, want 1.32 (from the Ready node)", got.KubernetesVersion)
	}
	if got.Provider != "AWS EKS" {
		t.Errorf("Provider = %q, want AWS EKS", got.Provider)
	}
}

const nodeRowFixture = `{"items":[
 {"metadata":{"name":"node-b","labels":{"topology.kubernetes.io/zone":"eu-west-1b"}},
  "status":{"capacity":{"cpu":"4","memory":"16Gi"},"allocatable":{"cpu":"3800m","memory":"15Gi"},
  "nodeInfo":{"architecture":"arm64"},"conditions":[{"type":"Ready","status":"True"}]}},
 {"metadata":{"name":"node-a","labels":{"topology.kubernetes.io/zone":"eu-west-1a"}},
  "status":{"capacity":{"cpu":"8","memory":"32Gi","nvidia.com/gpu":"2"},"allocatable":{"cpu":"8","memory":"31Gi","nvidia.com/gpu":"2"},
  "nodeInfo":{"architecture":"amd64"},"conditions":[{"type":"Ready","status":"False"}]}}
]}`

func TestBuildNodeStatsRows(t *testing.T) {
	got := buildStats(decodeNodes(t, nodeRowFixture))
	if len(got.Rows) != 2 {
		t.Fatalf("len(Rows) = %d, want 2", len(got.Rows))
	}
	// NotReady first, regardless of name.
	if got.Rows[0].Name != "node-a" || got.Rows[0].Ready {
		t.Errorf("Rows[0] = %+v, want not-ready node-a first", got.Rows[0])
	}
	if got.Rows[0].GPU != 2 || got.Rows[0].GPUResource != "nvidia.com/gpu" {
		t.Errorf("Rows[0] GPU = %d/%q, want 2/nvidia.com/gpu", got.Rows[0].GPU, got.Rows[0].GPUResource)
	}
	if got.Rows[0].CPU != "8" || got.Rows[0].Memory != "31.0" {
		t.Errorf("Rows[0] CPU/Memory = %s/%s, want 8/31.0", got.Rows[0].CPU, got.Rows[0].Memory)
	}
	if got.Rows[1].Name != "node-b" || !got.Rows[1].Ready {
		t.Errorf("Rows[1] = %+v, want ready node-b second", got.Rows[1])
	}
	if got.Rows[1].CPU != "3.8" || got.Rows[1].Zone != "eu-west-1b" {
		t.Errorf("Rows[1] CPU/Zone = %s/%s, want 3.8/eu-west-1b", got.Rows[1].CPU, got.Rows[1].Zone)
	}
	if got.Rows[1].GPUResource != "" || got.Rows[1].GPU != 0 {
		t.Errorf("Rows[1] should have no GPU, got %+v", got.Rows[1])
	}
}
