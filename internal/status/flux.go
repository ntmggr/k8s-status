package status

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// Source names the GitOps controller a row was read from.
type Source string

const (
	SourceArgoCD Source = "argocd"
	SourceFlux   Source = "flux"
)

// Condition types and values from the fluxcd/pkg/apis/meta vocabulary. Flux uses the
// standard metav1.Condition shape, so the value is a string, not a boolean.
const (
	condReady       = "Ready"
	condReconciling = "Reconciling"
	condStalled     = "Stalled"

	condTrue  = "True"
	condFalse = "False"
)

// Health words. Flux has no health vocabulary of its own, so the conditions are folded
// onto the words the ArgoCD rows already use, keeping one column meaningful for both.
const (
	healthHealthy     = "Healthy"
	healthDegraded    = "Degraded"
	healthProgressing = "Progressing"
	healthSuspended   = "Suspended"
)

// FluxSection reports what the Flux read returned. Error is set when the read failed or
// returned only one of the two kinds; the rest of the page is unaffected either way.
type FluxSection struct {
	HelmReleases   int
	Kustomizations int
	Denied         bool
	// Missing means the API server does not serve the Flux groups at all, which is the
	// normal answer on a cluster that does not run Flux rather than a fault.
	Missing bool
	Error   string
	// Root is the Flux equivalent of the environment header ArgoCD's root Application
	// powers, populated only when detectFluxRoot found a single Kustomization it can
	// point to without guessing. Nil otherwise, same as HasRoot being false for ArgoCD.
	Root *FluxRoot
}

// FluxRoot is one Kustomization's tracked revision, standing in for "what git state is
// this cluster at" the way ArgoCD's root Application does. Unlike the ArgoCD case, this
// is never the whole story: other Kustomizations and every HelmRelease may track
// something else entirely, so it is only ever shown alongside a label naming which
// Kustomization it came from, never as an unqualified cluster-wide fact.
type FluxRoot struct {
	Name   string
	Ref    string
	SHA    string
	Health string
	Detail string
}

// BuildFluxServices maps Flux objects onto the same Service shape the ArgoCD rows use.
//
// Flux does not compare running manifests against git the way ArgoCD does, so these
// rows carry no Sync value and can never land in DRIFT or PRUNE. Inventing a value for
// either would be a guess presented as a fact.
func BuildFluxServices(list *kube.FluxList, opts Options) []Service {
	if list == nil {
		return nil
	}
	out := make([]Service, 0, len(list.HelmReleases)+len(list.Kustomizations))

	for _, hr := range list.HelmReleases {
		st, health, detail := fluxVerdict(hr.Spec.Suspend, hr.Status.Conditions)
		out = append(out, Service{
			Name:       hr.Metadata.Name,
			Namespace:  hr.Metadata.Namespace,
			Kind:       kube.KindHelmRelease,
			Source:     SourceFlux,
			Version:    strings.TrimSpace(hr.Spec.Chart.Spec.Version),
			AppVersion: helmReleaseVersion(hr),
			GPU:        matchesAny(hr.Metadata.Name, opts.GPUGlobs),
			State:      st,
			Health:     health,
			Detail:     truncate(detail, maxTextLen),
		})
	}

	for _, k := range list.Kustomizations {
		st, health, detail := fluxVerdict(k.Spec.Suspend, k.Status.Conditions)
		ref, sha := parseFluxRevision(k.Status.LastAppliedRevision)
		out = append(out, Service{
			Name:      k.Metadata.Name,
			Namespace: k.Metadata.Namespace,
			Kind:      kube.KindKustomization,
			Source:    SourceFlux,
			Version:   ref,
			Revision:  sha,
			GPU:       matchesAny(k.Metadata.Name, opts.GPUGlobs),
			State:     st,
			Health:    health,
			Detail:    truncate(detail, maxTextLen),
		})
	}
	return out
}

// AppendFlux folds a Flux read into the snapshot: its rows join the service table and
// the summary, and a failed or partial read becomes an inline note.
//
// It deliberately returns nothing. Flux is an optional source, so a denied or absent
// read must degrade exactly as NODE_STATS and UNMANAGED do, never blank the page.
func (s *Snapshot) AppendFlux(list *kube.FluxList, err error, opts Options) {
	sec := FluxSection{}
	if list != nil {
		sec.HelmReleases = len(list.HelmReleases)
		sec.Kustomizations = len(list.Kustomizations)
		if root := detectFluxRoot(list.Kustomizations); root != nil {
			ref, sha := parseFluxRevision(root.Status.LastAppliedRevision)
			_, health, detail := fluxVerdict(root.Spec.Suspend, root.Status.Conditions)
			sec.Root = &FluxRoot{Name: root.Metadata.Name, Ref: ref, SHA: sha, Health: health, Detail: detail}
		}
	}

	for _, svc := range BuildFluxServices(list, opts) {
		if matchesAny(svc.Name, opts.IgnoreGlobs) {
			s.Summary.Hidden++
			continue
		}
		s.addService(svc)
	}

	if err != nil {
		sec.Error = truncate(kube.Sanitize(err.Error()), maxTextLen)
		var se *kube.StatusError
		if errors.As(err, &se) {
			sec.Denied = se.Code == http.StatusForbidden || se.Code == http.StatusUnauthorized
			sec.Missing = se.Code == http.StatusNotFound
		}
	}

	s.Flux = &sec
	sortServices(s.Services)
}

// fluxVerdict turns spec.suspend and status.conditions into a state, a health word and
// a line of detail. The order matters and mirrors how Flux itself reports:
//
//   - suspend wins outright, a suspended object keeps whatever conditions it had when
//     it was paused, so reading them would report stale news as current;
//   - Ready=False is a real failure and outranks everything else;
//   - Reconciling=True is checked before Ready=True, because Flux leaves the previous
//     Ready=True in place while a new reconciliation runs.
func fluxVerdict(suspend bool, conds []kube.Condition) (state State, health, detail string) {
	if suspend {
		return StateSuspended, healthSuspended, "reconciliation suspended (spec.suspend)"
	}

	ready, hasReady := findCondition(conds, condReady)
	reconciling, _ := findCondition(conds, condReconciling)
	stalled, _ := findCondition(conds, condStalled)

	switch {
	case hasReady && ready.Status == condFalse:
		text := conditionText(ready)
		if stalled.Status == condTrue {
			// Stalled means Flux has stopped retrying, so this will not fix itself.
			text = "stalled, no further retries: " + text
		}
		return StateDegraded, healthDegraded, text
	case stalled.Status == condTrue:
		return StateWarning, healthUnknown, "stalled, no further retries: " + conditionText(stalled)
	case reconciling.Status == condTrue:
		return StateProgressing, healthProgressing, conditionText(reconciling)
	case hasReady && ready.Status == condTrue:
		return StateOK, healthHealthy, ""
	case hasReady:
		return StateWarning, healthUnknown, conditionText(ready)
	default:
		return StateWarning, healthUnknown, "no Ready condition reported yet"
	}
}

func findCondition(conds []kube.Condition, kind string) (kube.Condition, bool) {
	for _, c := range conds {
		if c.Type == kind {
			return c, true
		}
	}
	return kube.Condition{}, false
}

// conditionText joins a condition's reason and message into one readable line.
func conditionText(c kube.Condition) string {
	reason := strings.TrimSpace(c.Reason)
	msg := strings.TrimSpace(c.Message)
	switch {
	case reason != "" && msg != "":
		return reason + ": " + msg
	case msg != "":
		return msg
	default:
		return reason
	}
}

// helmReleaseVersion is the version of the application a HelmRelease has installed.
//
// A HelmRelease has no git revision of any kind: helm-controller records chart versions.
// status.history[0] is the live release and is the only field that describes what is
// actually running, so it wins; its appVersion is the application's own version and is
// the closest counterpart to the image tag an ArgoCD row shows, with the chart version
// as the fallback when a chart declares no appVersion.
//
// The remaining two are last resorts and are not interchangeable:
// status.lastAttemptedRevision is the chart version of the most recent attempt, which
// may have failed, and status.lastAppliedRevision only exists on helm-controller v0.x.
func helmReleaseVersion(hr kube.HelmRelease) string {
	if len(hr.Status.History) > 0 {
		h := hr.Status.History[0]
		if v := strings.TrimSpace(h.AppVersion); v != "" {
			return v
		}
		if v := strings.TrimSpace(h.ChartVersion); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(hr.Status.LastAttemptedRevision); v != "" {
		return v
	}
	return strings.TrimSpace(hr.Status.LastAppliedRevision)
}

// parseFluxRevision splits a Flux source revision into its branch or tag and its commit.
// Current controllers write "<ref>@<algo>:<digest>", for example "main@sha1:a1b2c3d";
// older ones wrote "<ref>/<digest>". Either half may be missing, and a bare digest is
// recognised so it is not mistaken for a branch name.
func parseFluxRevision(rev string) (ref, sha string) {
	rev = strings.TrimSpace(rev)
	if rev == "" {
		return "", ""
	}

	switch i := strings.LastIndex(rev, "@"); {
	case i >= 0:
		ref, sha = rev[:i], rev[i+1:]
	case strings.Contains(rev, ":"), looksLikeDigest(rev):
		sha = rev
	default:
		if j := strings.LastIndex(rev, "/"); j >= 0 && looksLikeDigest(rev[j+1:]) {
			ref, sha = rev[:j], rev[j+1:]
		} else {
			ref = rev
		}
	}

	// The digest carries its algorithm ("sha1:", "sha256:"); the page shows the digest.
	if i := strings.Index(sha, ":"); i >= 0 {
		sha = sha[i+1:]
	}
	return ref, sha
}

// fluxRootName is the name flux bootstrap gives the Kustomization that applies the
// rest of a cluster's manifests: the conventional entry point of a typical bootstrap
// tree, and the one reasonable name-based guess when several Kustomizations exist.
const fluxRootName = "flux-system"

// detectFluxRoot returns the Kustomization equivalent of ArgoCD's root Application, or
// nil when there is no unambiguous one to point to.
//
// Flux keeps no single object that stands for "the whole cluster's git state" the way
// ArgoCD's root Application does: there can be zero, one or many Kustomizations, each
// syncing its own revision. This only resolves to one when the answer is not a guess:
// there is exactly one Kustomization, or one of several is named fluxRootName. Anything
// else -- none, or several with no such candidate -- returns nil rather than presenting
// an arbitrary pick as fact.
func detectFluxRoot(kustomizations []kube.Kustomization) *kube.Kustomization {
	if len(kustomizations) == 1 {
		return &kustomizations[0]
	}
	for i := range kustomizations {
		if kustomizations[i].Metadata.Name == fluxRootName {
			return &kustomizations[i]
		}
	}
	return nil
}

// looksLikeDigest reports whether s is a bare commit hash rather than a ref name.
func looksLikeDigest(s string) bool {
	if len(s) < 7 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
