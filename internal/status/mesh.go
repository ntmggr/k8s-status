package status

import (
	"errors"
	"net/http"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// Istio's own spelling of spec.mtls.mode.
const (
	istioModeStrict     = "STRICT"
	istioModePermissive = "PERMISSIVE"
	istioModeDisable    = "DISABLE"
)

// Effective mTLS words this page renders, independent of Istio's own spelling.
const (
	MeshStrict     = "strict"
	MeshPermissive = "permissive"
	MeshDisabled   = "disabled"
	// MeshNone means no service mesh was detected at all. A deliberate architecture
	// choice, not a finding, so it must never render as a warning or an error.
	MeshNone = "none"
)

// MeshSection reports the cluster-wide Istio mTLS posture. Error is set when the read
// failed; the rest of the page is unaffected either way.
type MeshSection struct {
	Installed  bool
	APIVersion string
	// Mode is the literal spec.mtls.mode string, "" if the object exists but the field
	// is absent or set to UNSET.
	Mode string
	// Effective is the mesh-wide mTLS posture once Istio's own defaults are applied:
	// "strict", "permissive", "disabled" or "none".
	Effective string
	// PolicyFound is false when Istio is installed but no "default" PeerAuthentication
	// object exists. That is not a fault: Istio's built-in PERMISSIVE default applies
	// instead, which Effective already reflects.
	PolicyFound bool
	// Scoped is true when the "default"-named object carries a non-empty selector,
	// meaning it is actually scoped to a workload and not mesh-wide despite the name.
	Scoped bool
	Denied bool
	// Missing means no Istio PeerAuthentication CRD is served at all, the same
	// "absent, not a fault" meaning FluxSection.Missing carries.
	Missing bool
	Error   string
}

// State maps Effective onto this package's shared severity colors, for color only:
// Effective is the word that actually carries the fact.
func (m MeshSection) State() State {
	switch m.Effective {
	case MeshStrict:
		return StateOK
	case MeshPermissive:
		return StateWarning
	case MeshDisabled:
		return StateDegraded
	default:
		// No mesh is a legitimate architecture choice: reuse SUSPENDED's neutral
		// gray rather than inventing a new color pair for "not applicable".
		return StateSuspended
	}
}

// BuildMesh classifies a mesh policy read against Istio's actual defaults.
//
// Every branch here was checked against real Istio behaviour, not guessed, and the
// order matters:
//   - installed==false is a legitimate architecture choice and short-circuits before
//     anything else, so it never picks up an Effective the rest of this function would
//     otherwise assign to an error;
//   - a 404 on the object itself is Istio's normal answer for "no PeerAuthentication
//     configured", and its mesh-wide default is PERMISSIVE, not "unknown";
//   - 403/401 gets no Effective at all, because no fact about mTLS was actually read.
func BuildMesh(installed bool, apiVersion string, pa *kube.PeerAuthentication, err error) MeshSection {
	sec := MeshSection{Installed: installed, APIVersion: apiVersion}
	if !installed {
		sec.Effective = MeshNone
		sec.Missing = true
		return sec
	}

	if err != nil {
		var se *kube.StatusError
		if errors.As(err, &se) {
			switch se.Code {
			case http.StatusNotFound:
				sec.Effective = MeshPermissive
				return sec
			case http.StatusForbidden, http.StatusUnauthorized:
				sec.Denied = true
				sec.Error = truncate(kube.Sanitize(err.Error()), maxTextLen)
				return sec
			}
		}
		sec.Error = truncate(kube.Sanitize(err.Error()), maxTextLen)
		return sec
	}

	sec.PolicyFound = true
	if pa != nil {
		sec.Mode = pa.Spec.MTLS.Mode
		sec.Scoped = len(pa.Spec.Selector.MatchLabels) > 0
	}

	switch sec.Mode {
	case istioModeStrict:
		sec.Effective = MeshStrict
	case istioModePermissive:
		sec.Effective = MeshPermissive
	case istioModeDisable:
		sec.Effective = MeshDisabled
	default:
		// UNSET, or the field absent, inherits the parent: at mesh level the parent is
		// Istio's PERMISSIVE built-in default.
		sec.Effective = MeshPermissive
	}
	return sec
}

// PolicyScope says which level of Istio's PeerAuthentication hierarchy actually
// produced a workload's effective mTLS mode. The word alone ("permissive") reads the
// same whether it is the cluster default or a deliberate override; the scope is what
// turns that into an honest tooltip -- "this service has its own workload-level
// override" is a very different fact from "inherits the cluster-wide default", even
// when they render the same colour.
type PolicyScope string

const (
	ScopeMesh      PolicyScope = "mesh"
	ScopeNamespace PolicyScope = "namespace"
	ScopeWorkload  PolicyScope = "workload"
)

// ServicePolicy is one workload's own effective mTLS mode, resolved against Istio's
// precedence rules, and which scope actually produced it.
type ServicePolicy struct {
	// Effective is one of MeshStrict/MeshPermissive/MeshDisabled, or "" when it was
	// never resolved (AZ_SPREAD off, or the workload matched no running pod).
	Effective string
	Scope     PolicyScope
}

// Known reports whether this workload has a usable policy answer at all.
func (p ServicePolicy) Known() bool { return p.Scope != "" }

// mtlsMode maps Istio's own spec.mtls.mode spelling onto this page's effective words.
// ok is false for UNSET or an absent field, which is not a mode this scope sets: the
// caller must keep looking at the next broader scope rather than treat it as a match.
//
// This deliberately duplicates the four-way switch inside BuildMesh rather than
// sharing it: BuildMesh's UNSET branch falls back to Istio's mesh-wide PERMISSIVE
// default because there is nothing broader than the mesh, while at namespace or
// workload scope UNSET must keep searching outward instead, a different fallback
// with the same four inputs. Extracting a shared helper that both callers can use
// correctly needs an explicit fallback parameter, which reads as more machinery than
// nine lines of duplication saves.
func mtlsMode(mode string) (string, bool) {
	switch mode {
	case istioModeStrict:
		return MeshStrict, true
	case istioModePermissive:
		return MeshPermissive, true
	case istioModeDisable:
		return MeshDisabled, true
	default:
		return "", false
	}
}

// selectorMatches reports whether every label a PeerAuthentication's selector
// requires is present on the pod with the same value. Istio's WorkloadSelector is an
// AND of matchLabels, same as a Kubernetes label selector without matchExpressions,
// and PeerAuthentication supports no matchExpressions at all.
func selectorMatches(selector, podLabels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if podLabels[k] != v {
			return false
		}
	}
	return true
}

// ResolveServicePolicy resolves one workload's effective mTLS mode against Istio's
// precedence rules:
// https://istio.io/latest/docs/reference/config/security/peer_authentication/
//
//  1. workload-specific: a PeerAuthentication in the workload's own namespace whose
//     selector matches podLabels.
//  2. namespace-wide: a PeerAuthentication in the workload's own namespace with an
//     empty selector.
//  3. mesh-wide: meshEffective, the answer BuildMesh already computed (which itself
//     already folds in Istio's PERMISSIVE built-in default, so there is no separate
//     fallback step here).
//
// UNSET at a narrower scope does not stop the search, it falls through to the next
// broader one exactly as if that scope had no policy at all: a workload-scoped object
// with mode UNSET still falls through to the namespace-wide one, not straight to mesh.
//
// A workload living in the mesh namespace itself is always mesh-scoped: Istio ignores
// a selector on a PeerAuthentication placed in the root namespace (mesh-level policies
// cannot be workload-scoped, the same rule BuildMesh's own Scoped field flags for the
// mesh-wide object), so there is no namespace- or workload-level override to look for
// there.
//
// If more than one workload-scoped object in the namespace matches podLabels, Istio's
// own behaviour is undefined. This picks whichever comes first in list order,
// deterministically -- not because list order carries meaning, only because a
// consistent answer beats a random one when Istio itself promises neither.
func ResolveServicePolicy(list *kube.PeerAuthenticationList, meshNamespace, workloadNamespace string, podLabels map[string]string, meshEffective string) ServicePolicy {
	fallback := ServicePolicy{Effective: meshEffective, Scope: ScopeMesh}
	if list == nil || workloadNamespace == "" || workloadNamespace == meshNamespace {
		return fallback
	}

	var workloadMatch, namespaceWide *kube.PeerAuthentication
	for i := range list.Items {
		pa := &list.Items[i]
		if pa.Metadata.Namespace != workloadNamespace {
			continue
		}
		if len(pa.Spec.Selector.MatchLabels) == 0 {
			if namespaceWide == nil {
				namespaceWide = pa
			}
			continue
		}
		if workloadMatch == nil && selectorMatches(pa.Spec.Selector.MatchLabels, podLabels) {
			workloadMatch = pa
		}
	}

	if workloadMatch != nil {
		if eff, ok := mtlsMode(workloadMatch.Spec.MTLS.Mode); ok {
			return ServicePolicy{Effective: eff, Scope: ScopeWorkload}
		}
	}
	if namespaceWide != nil {
		if eff, ok := mtlsMode(namespaceWide.Spec.MTLS.Mode); ok {
			return ServicePolicy{Effective: eff, Scope: ScopeNamespace}
		}
	}
	return fallback
}
