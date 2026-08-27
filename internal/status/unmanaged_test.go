package status

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func dep(name, ns string, mutate func(*kube.Workload)) kube.Workload {
	w := kube.Workload{
		Kind: kube.KindDeployment,
		Metadata: kube.WorkloadMetadata{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{},
		},
		Status: kube.WorkloadStatus{Replicas: 1, ReadyReplicas: 1},
	}
	if mutate != nil {
		mutate(&w)
	}
	return w
}

func names(u Unmanaged) []string {
	out := make([]string, 0, len(u.Items))
	for _, w := range u.Items {
		out = append(out, w.Name)
	}
	return out
}

// Each ArgoCD ownership marker on its own must be enough to keep a workload out of the
// list, and so must an ownerReference.
func TestUnmanagedDetectionRule(t *testing.T) {
	list := &kube.WorkloadList{Items: []kube.Workload{
		dep("plain", "ns", nil),
		dep("tracked", "ns", func(w *kube.Workload) {
			w.Metadata.Annotations = map[string]string{annTrackingID: "svc:apps/Deployment:ns/tracked"}
		}),
		dep("instance", "ns", func(w *kube.Workload) {
			w.Metadata.Labels[labelInstance] = "svc"
		}),
		dep("argo-instance", "ns", func(w *kube.Workload) {
			w.Metadata.Labels[labelArgoInstance] = "svc"
		}),
		dep("owned", "ns", func(w *kube.Workload) {
			w.Metadata.OwnerReferences = []kube.OwnerReference{{Kind: "Autoc", Name: "operator"}}
		}),
	}}

	got := BuildUnmanaged(list, Options{})

	if got.Scanned != 5 {
		t.Errorf("scanned = %d, want 5", got.Scanned)
	}
	if got.Count != 1 || len(got.Items) != 1 || got.Items[0].Name != "plain" {
		t.Fatalf("items = %v, want only [plain]", names(got))
	}
}

// The ownerReferences half of the rule is the one a maintainer is tempted to drop.
// Without it an operator's spawned Deployments swamp the list.
func TestOwnerReferencesSuppressOperatorChurn(t *testing.T) {
	items := []kube.Workload{dep("real-infra", "kube-system", nil)}
	for i := 0; i < 50; i++ {
		items = append(items, dep("autoc-child", "apps", func(w *kube.Workload) {
			w.Metadata.OwnerReferences = []kube.OwnerReference{{Kind: "Autoc", Name: "operator"}}
		}))
	}

	got := BuildUnmanaged(&kube.WorkloadList{Items: items}, Options{})

	if got.Count != 1 {
		t.Errorf("count = %d, want 1: owned workloads must be excluded", got.Count)
	}
}

func TestReadinessPerKind(t *testing.T) {
	list := &kube.WorkloadList{Items: []kube.Workload{
		{Kind: kube.KindDeployment, Metadata: kube.WorkloadMetadata{Name: "d", Namespace: "ns"},
			Status: kube.WorkloadStatus{Replicas: 2, ReadyReplicas: 2}},
		{Kind: kube.KindStatefulSet, Metadata: kube.WorkloadMetadata{Name: "s", Namespace: "ns"},
			Status: kube.WorkloadStatus{Replicas: 3, ReadyReplicas: 1}},
		{Kind: kube.KindDaemonSet, Metadata: kube.WorkloadMetadata{Name: "x", Namespace: "ns"},
			Status: kube.WorkloadStatus{DesiredNumberScheduled: 82, NumberReady: 81}},
		// A Deployment that reports no readyReplicas at all: the field is absent, not zeroed.
		{Kind: kube.KindDeployment, Metadata: kube.WorkloadMetadata{Name: "missing", Namespace: "ns"},
			Status: kube.WorkloadStatus{Replicas: 1}},
		// A DaemonSet must not read the replica fields even when they are populated.
		{Kind: kube.KindDaemonSet, Metadata: kube.WorkloadMetadata{Name: "ds-no-fields", Namespace: "ns"},
			Status: kube.WorkloadStatus{Replicas: 9, ReadyReplicas: 9}},
	}}

	got := BuildUnmanaged(list, Options{})

	want := map[string][2]int{
		"d":            {2, 2},
		"s":            {1, 3},
		"x":            {81, 82},
		"missing":      {0, 1},
		"ds-no-fields": {0, 0},
	}
	if len(got.Items) != len(want) {
		t.Fatalf("items = %v", names(got))
	}
	for _, w := range got.Items {
		if [2]int{w.Ready, w.Desired} != want[w.Name] {
			t.Errorf("%s = %d/%d, want %d/%d", w.Name, w.Ready, w.Desired, want[w.Name][0], want[w.Name][1])
		}
	}
}

func TestWorkloadStateMapping(t *testing.T) {
	cases := []struct {
		ready, desired int
		want           State
	}{
		{1, 1, StateOK},
		{2, 2, StateOK},
		{81, 82, StateDegraded},
		{0, 2, StateDegraded},
		// 0/0 is a DaemonSet that matches no node, a Windows daemonset on a cluster
		// with no Windows nodes. Legitimate, not broken.
		{0, 0, StateSuspended},
	}
	for _, c := range cases {
		if got := workloadState(c.ready, c.desired); got != c.want {
			t.Errorf("state(%d/%d) = %s, want %s", c.ready, c.desired, got, c.want)
		}
	}
}

func TestUnmanagedNamespaceIgnoreGlobs(t *testing.T) {
	list := &kube.WorkloadList{Items: []kube.Workload{
		dep("a", "kube-system", nil),
		dep("b", "istio-system", nil),
		dep("c", "gpu-operator-resources", nil),
		dep("d", "mcp-server", nil),
	}}

	got := BuildUnmanaged(list, Options{UnmanagedIgnoreNS: []string{"kube-*", "istio-system"}})

	if got.Count != 2 {
		t.Fatalf("items = %v, want 2", names(got))
	}
	if got.Ignored != 2 {
		t.Errorf("ignored = %d, want 2", got.Ignored)
	}
	for _, w := range got.Items {
		if w.Namespace == "kube-system" || w.Namespace == "istio-system" {
			t.Errorf("%s/%s should have been ignored", w.Namespace, w.Name)
		}
	}
}

func TestUnmanagedManagedByAndVersion(t *testing.T) {
	list := &kube.WorkloadList{Items: []kube.Workload{
		dep("helm-thing", "ns", func(w *kube.Workload) {
			w.Metadata.Labels[labelManagedBy] = "Helm"
			w.Spec.Template.Spec.Containers = []kube.Container{{Image: "registry.example.invalid/org/thing:v1.4.2"}}
		}),
		dep("no-label", "ns", func(w *kube.Workload) {
			w.Spec.Template.Spec.Containers = []kube.Container{
				{Image: "registry.example.invalid/org/other@sha256:abc"},
				{Image: "registry.example.invalid/org/sidecar:9.9.9"},
			}
		}),
		dep("no-containers", "ns", nil),
	}}

	got := BuildUnmanaged(list, Options{})
	by := map[string]Workload{}
	for _, w := range got.Items {
		by[w.Name] = w
	}

	if by["helm-thing"].ManagedBy != "Helm" || by["helm-thing"].Version != "v1.4.2" {
		t.Errorf("helm-thing = %+v", by["helm-thing"])
	}
	// Empty renders as unknown, and a digest reference yields no tag.
	if by["no-label"].ManagedBy != managedByUnknown || by["no-label"].Version != "" {
		t.Errorf("no-label = %+v", by["no-label"])
	}
	if by["no-containers"].Version != "" || by["no-containers"].Image != "" {
		t.Errorf("no-containers = %+v", by["no-containers"])
	}
}

func TestUnmanagedSortedWorstFirstThenNamespaceThenName(t *testing.T) {
	list := &kube.WorkloadList{Items: []kube.Workload{
		dep("z-ok", "aaa", nil),
		dep("a-ok", "zzz", nil),
		dep("b-ok", "aaa", nil),
		dep("susp", "zzz", func(w *kube.Workload) { w.Status = kube.WorkloadStatus{} }),
		dep("bad", "zzz", func(w *kube.Workload) { w.Status = kube.WorkloadStatus{Replicas: 2} }),
	}}

	got := names(BuildUnmanaged(list, Options{}))

	// Same severity order the service table uses: DEGRADED, then SUSPENDED, then OK.
	want := []string{"bad", "susp", "b-ok", "z-ok", "a-ok"}
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestBuildUnmanagedNilAndEmpty(t *testing.T) {
	if got := BuildUnmanaged(nil, Options{}); got.Count != 0 || len(got.Items) != 0 {
		t.Errorf("nil list = %+v", got)
	}
	if got := BuildUnmanaged(&kube.WorkloadList{}, Options{}); got.Count != 0 || got.Scanned != 0 {
		t.Errorf("empty list = %+v", got)
	}
}

type fakeWorkloadLister struct {
	called int
	list   *kube.WorkloadList
	err    error
}

func (f *fakeWorkloadLister) ListWorkloads(context.Context) (*kube.WorkloadList, error) {
	f.called++
	return f.list, f.err
}

// The default deployment holds no ClusterRole, so a collector without WithUnmanaged
// must never reach for the cluster-wide workloads API.
func TestUnmanagedNotFetchedWhenDisabled(t *testing.T) {
	workloads := &fakeWorkloadLister{list: &kube.WorkloadList{Items: []kube.Workload{dep("a", "ns", nil)}}}
	c, _ := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snap.Unmanaged != nil {
		t.Errorf("Unmanaged = %+v, want nil when the feature is off", snap.Unmanaged)
	}
	if workloads.called != 0 {
		t.Errorf("workloads API called %d times, want 0", workloads.called)
	}
}

func TestUnmanagedFetchedOncePerTTL(t *testing.T) {
	workloads := &fakeWorkloadLister{list: &kube.WorkloadList{Items: []kube.Workload{dep("a", "ns", nil)}}}
	c, clock := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithUnmanaged(workloads)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snap.Unmanaged == nil || snap.Unmanaged.Count != 1 {
		t.Fatalf("Unmanaged = %+v", snap.Unmanaged)
	}

	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("cached get: %v", err)
	}
	if workloads.called != 1 {
		t.Errorf("workload fetches within TTL = %d, want 1", workloads.called)
	}

	clock.advance(16 * time.Second)
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("refresh get: %v", err)
	}
	if workloads.called != 2 {
		t.Errorf("workload fetches after TTL = %d, want 2", workloads.called)
	}
}

func TestUnmanagedDeniedDegradesInsteadOfFailing(t *testing.T) {
	workloads := &fakeWorkloadLister{err: &kube.StatusError{Code: http.StatusForbidden, Body: "deployments is forbidden"}}
	c, _ := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithUnmanaged(workloads)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("a denied workloads read must not fail the snapshot: %v", err)
	}
	if snap.Summary.Total != 14 {
		t.Errorf("application data lost: total = %d, want 14", snap.Summary.Total)
	}
	if snap.Unmanaged == nil {
		t.Fatal("want an Unmanaged carrying the error")
	}
	if !snap.Unmanaged.Denied {
		t.Error("403 should be reported as denied")
	}
	if snap.Unmanaged.Error == "" {
		t.Error("want the error text surfaced in the section")
	}
}

// A partial read keeps the kinds that answered and still notes the failure.
func TestUnmanagedPartialReadKeepsRows(t *testing.T) {
	workloads := &fakeWorkloadLister{
		list: &kube.WorkloadList{Items: []kube.Workload{dep("a", "ns", nil)}},
		err:  errors.Join(&kube.StatusError{Code: http.StatusForbidden, Body: "statefulsets is forbidden"}),
	}
	c, _ := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithUnmanaged(workloads)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snap.Unmanaged.Count != 1 {
		t.Errorf("count = %d, want the row that was read", snap.Unmanaged.Count)
	}
	if snap.Unmanaged.Error == "" || !snap.Unmanaged.Denied {
		t.Errorf("want the partial failure noted: %+v", snap.Unmanaged)
	}
}

func TestUnmanagedTransportErrorIsNotDenied(t *testing.T) {
	workloads := &fakeWorkloadLister{err: errors.New("query kubernetes api: connection refused")}
	c, _ := newTestCollector(t, &fakeLister{list: loadFixture(t)}, 15*time.Second)
	c.WithUnmanaged(workloads)

	snap, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snap.Unmanaged.Denied {
		t.Error("a transport error is not an RBAC denial")
	}
}

// Helm sets app.kubernetes.io/instance on everything it installs, so that label
// alone must not count as ArgoCD ownership. istiod is the real-world case: Helm
// installed, no ArgoCD marker, and it belongs in the unmanaged list.
func TestHelmReleaseIsNotMistakenForArgoCD(t *testing.T) {
	helm := kube.Workload{Kind: "Deployment"}
	helm.Metadata.Name = "istiod"
	helm.Metadata.Namespace = "istio-system"
	helm.Metadata.Labels = map[string]string{
		"app.kubernetes.io/instance":   "istiod",
		"app.kubernetes.io/managed-by": "Helm",
	}
	if !isUnmanaged(helm) {
		t.Error("a Helm release with no ArgoCD marker must be reported as unmanaged")
	}

	// ArgoCD's default tracking method also uses that label. Without managed-by=Helm
	// it still means ArgoCD, and must stay out of the list.
	argo := helm
	argo.Metadata.Labels = map[string]string{"app.kubernetes.io/instance": "some-app"}
	if isUnmanaged(argo) {
		t.Error("app.kubernetes.io/instance without Helm should still count as ArgoCD-tracked")
	}
}

// Seven Deployments from one Helm chart are one thing to know about, not seven.
func TestHelmReleaseCollapsesToOneRow(t *testing.T) {
	mk := func(name, ver string, ready, desired int) kube.Workload {
		w := kube.Workload{Kind: "Deployment"}
		w.Metadata.Name = name
		w.Metadata.Namespace = "argocd"
		w.Metadata.Labels = map[string]string{
			"app.kubernetes.io/instance":   "argocd",
			"app.kubernetes.io/managed-by": "Helm",
			"app.kubernetes.io/version":    ver,
		}
		w.Status.ReadyReplicas, w.Status.Replicas = ready, desired
		return w
	}
	list := &kube.WorkloadList{Items: []kube.Workload{
		mk("argocd-server", "v3.4.6", 1, 1),
		mk("argocd-repo-server", "v3.4.6", 1, 1),
		mk("argocd-redis", "v3.4.6", 0, 1), // one member unhealthy
	}}

	u := BuildUnmanaged(list, Options{})
	if u.Count != 1 {
		t.Fatalf("count = %d, want 1 collapsed row", u.Count)
	}
	row := u.Items[0]
	if row.Name != "argocd" || row.Kind != "release" {
		t.Errorf("row = %q/%q, want argocd/release", row.Name, row.Kind)
	}
	if row.Members != 3 {
		t.Errorf("members = %d, want 3", row.Members)
	}
	if row.Ready != 2 || row.Desired != 3 {
		t.Errorf("readiness = %d/%d, want 2/3 summed", row.Ready, row.Desired)
	}
	// A broken member must not be hidden by healthy siblings.
	if row.State != StateDegraded {
		t.Errorf("state = %s, want DEGRADED from the worst member", row.State)
	}
	// Helm records one version for the release; do not report "mixed" from image tags.
	if row.Version != "v3.4.6" {
		t.Errorf("version = %q, want v3.4.6", row.Version)
	}
}

// Anything Helm did not install stays on its own row.
func TestNonHelmWorkloadsAreNotCollapsed(t *testing.T) {
	mk := func(name string) kube.Workload {
		w := kube.Workload{Kind: "DaemonSet"}
		w.Metadata.Name, w.Metadata.Namespace = name, "kube-system"
		return w
	}
	u := BuildUnmanaged(&kube.WorkloadList{Items: []kube.Workload{mk("kube-proxy"), mk("ebs-csi-node")}}, Options{})
	if u.Count != 2 {
		t.Fatalf("count = %d, want 2 separate rows", u.Count)
	}
	for _, w := range u.Items {
		if w.Members != 1 || w.Kind == "release" {
			t.Errorf("%s was collapsed but is not a Helm release", w.Name)
		}
	}
}
