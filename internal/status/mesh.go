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
		sec.Scoped = len(pa.Spec.Selector) > 0
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
