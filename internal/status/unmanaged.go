package status

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// ArgoCD stamps every resource it owns with one of these. Any of them present means
// the workload is under GitOps and belongs in the service table, not here.
const (
	annTrackingID     = "argocd.argoproj.io/tracking-id"
	labelInstance     = "app.kubernetes.io/instance"
	labelArgoInstance = "argocd.argoproj.io/instance"
)

// labelManagedBy is the standard "who installed this" label: Helm, EKS, terraform, or
// absent.
const labelManagedBy = "app.kubernetes.io/managed-by"

const managedByUnknown = "unknown"

// Workload is one running thing that ArgoCD does not manage.
type Workload struct {
	Namespace string
	Kind      string
	Name      string
	ManagedBy string
	Ready     int
	Desired   int
	Version   string
	Image     string
	State     State
}

// Unmanaged is the set of workloads running outside GitOps. Error is set when the
// workloads read failed or returned only part of the answer; the rest of the page is
// unaffected either way.
type Unmanaged struct {
	Items   []Workload
	Count   int
	Scanned int
	Ignored int
	Denied  bool
	Error   string
}

// BuildUnmanaged selects the workloads that carry no ArgoCD ownership marker and have
// no owner of their own.
func BuildUnmanaged(list *kube.WorkloadList, opts Options) Unmanaged {
	out := Unmanaged{Items: []Workload{}}
	if list == nil {
		return out
	}

	for _, w := range list.Items {
		out.Scanned++
		if !isUnmanaged(w) {
			continue
		}
		if matchesAny(w.Metadata.Namespace, opts.UnmanagedIgnoreNS) {
			out.Ignored++
			continue
		}

		ready, desired := readiness(w)
		img := parseImage(firstImage(w))
		out.Items = append(out.Items, Workload{
			Namespace: w.Metadata.Namespace,
			Kind:      w.Kind,
			Name:      w.Metadata.Name,
			ManagedBy: managedBy(w),
			Ready:     ready,
			Desired:   desired,
			Version:   img.Tag,
			Image:     img.Full,
			State:     workloadState(ready, desired),
		})
	}
	out.Count = len(out.Items)

	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if severity[a.State] != severity[b.State] {
			return severity[a.State] < severity[b.State]
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}

// isUnmanaged applies both halves of the rule, and both halves are load-bearing.
//
// The ownership markers alone are not enough. Most of what they miss is not
// infrastructure but churn: pods spawned by an in-cluster operator inherit no ArgoCD
// label, so a marker-only rule listed 292 workloads on a live cluster, nearly all of
// them per-request Deployments created by another controller. Requiring an empty
// metadata.ownerReferences drops every one of those — a workload with an owner was
// created by something already running in the cluster, not installed into it — and
// leaves 11 rows that are all real infrastructure. Do not remove the ownerReferences
// check to "simplify" this; the list becomes unreadable and the signal is lost.
func isUnmanaged(w kube.Workload) bool {
	if len(w.Metadata.OwnerReferences) > 0 {
		return false
	}
	if _, ok := w.Metadata.Annotations[annTrackingID]; ok {
		return false
	}
	if _, ok := w.Metadata.Labels[labelInstance]; ok {
		return false
	}
	if _, ok := w.Metadata.Labels[labelArgoInstance]; ok {
		return false
	}
	return true
}

// readiness picks the counters the kind actually populates.
func readiness(w kube.Workload) (ready, desired int) {
	if w.Kind == kube.KindDaemonSet {
		return w.Status.NumberReady, w.Status.DesiredNumberScheduled
	}
	return w.Status.ReadyReplicas, w.Status.Replicas
}

// workloadState maps the counters onto the page's existing states. desired == 0 is
// SUSPENDED, not broken: a DaemonSet whose node selector matches no node — a Windows
// daemonset on a cluster with no Windows nodes — is legitimately 0/0.
func workloadState(ready, desired int) State {
	switch {
	case desired <= 0:
		return StateSuspended
	case ready < desired:
		return StateDegraded
	default:
		return StateOK
	}
}

func managedBy(w kube.Workload) string {
	if v := strings.TrimSpace(w.Metadata.Labels[labelManagedBy]); v != "" {
		return v
	}
	return managedByUnknown
}

func firstImage(w kube.Workload) string {
	if len(w.Spec.Template.Spec.Containers) == 0 {
		return ""
	}
	return w.Spec.Template.Spec.Containers[0].Image
}

// unmanagedError builds the degraded form of the section: whatever was read, plus one
// note. A partial read keeps its rows.
func unmanagedError(u Unmanaged, err error) Unmanaged {
	u.Error = truncate(kube.Sanitize(err.Error()), maxTextLen)
	var se *kube.StatusError
	if errors.As(err, &se) {
		u.Denied = se.Code == http.StatusForbidden || se.Code == http.StatusUnauthorized
	}
	return u
}
