package status

import (
	"errors"
	"net/http"
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func TestBuildMesh(t *testing.T) {
	forbidden := &kube.StatusError{Code: http.StatusForbidden, Body: "peerauthentications is forbidden"}
	notFound := &kube.StatusError{Code: http.StatusNotFound, Body: "not found"}
	otherErr := errors.New("dial tcp: connection refused")

	tests := []struct {
		name       string
		installed  bool
		apiVersion string
		pa         *kube.PeerAuthentication
		err        error

		wantEffective   string
		wantMissing     bool
		wantDenied      bool
		wantPolicyFound bool
		wantScoped      bool
		wantErrorSet    bool
	}{
		{
			name:          "not installed",
			installed:     false,
			wantEffective: MeshNone,
			wantMissing:   true,
		},
		{
			name:          "installed, no policy object (404)",
			installed:     true,
			apiVersion:    kube.IstioSecurityGroupVersion,
			err:           notFound,
			wantEffective: MeshPermissive,
		},
		{
			name:            "mode STRICT",
			installed:       true,
			pa:              &kube.PeerAuthentication{Spec: kube.PeerAuthenticationSpec{MTLS: kube.PeerAuthenticationMTLS{Mode: "STRICT"}}},
			wantEffective:   MeshStrict,
			wantPolicyFound: true,
		},
		{
			name:            "mode PERMISSIVE",
			installed:       true,
			pa:              &kube.PeerAuthentication{Spec: kube.PeerAuthenticationSpec{MTLS: kube.PeerAuthenticationMTLS{Mode: "PERMISSIVE"}}},
			wantEffective:   MeshPermissive,
			wantPolicyFound: true,
		},
		{
			name:            "mode DISABLE",
			installed:       true,
			pa:              &kube.PeerAuthentication{Spec: kube.PeerAuthenticationSpec{MTLS: kube.PeerAuthenticationMTLS{Mode: "DISABLE"}}},
			wantEffective:   MeshDisabled,
			wantPolicyFound: true,
		},
		{
			name:            "mode UNSET",
			installed:       true,
			pa:              &kube.PeerAuthentication{Spec: kube.PeerAuthenticationSpec{MTLS: kube.PeerAuthenticationMTLS{Mode: "UNSET"}}},
			wantEffective:   MeshPermissive,
			wantPolicyFound: true,
		},
		{
			name:            "mode absent entirely",
			installed:       true,
			pa:              &kube.PeerAuthentication{},
			wantEffective:   MeshPermissive,
			wantPolicyFound: true,
		},
		{
			name:      "selector present alongside STRICT",
			installed: true,
			pa: &kube.PeerAuthentication{Spec: kube.PeerAuthenticationSpec{
				MTLS:     kube.PeerAuthenticationMTLS{Mode: "STRICT"},
				Selector: kube.PeerAuthenticationSelector{MatchLabels: map[string]string{"app": "payments"}},
			}},
			wantEffective:   MeshStrict,
			wantPolicyFound: true,
			wantScoped:      true,
		},
		{
			name:         "403 forbidden",
			installed:    true,
			err:          forbidden,
			wantDenied:   true,
			wantErrorSet: true,
		},
		{
			name:         "other error",
			installed:    true,
			err:          otherErr,
			wantErrorSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sec := BuildMesh(tt.installed, tt.apiVersion, tt.pa, tt.err)
			if sec.Effective != tt.wantEffective {
				t.Errorf("Effective = %q, want %q", sec.Effective, tt.wantEffective)
			}
			if sec.Missing != tt.wantMissing {
				t.Errorf("Missing = %v, want %v", sec.Missing, tt.wantMissing)
			}
			if sec.Denied != tt.wantDenied {
				t.Errorf("Denied = %v, want %v", sec.Denied, tt.wantDenied)
			}
			if sec.PolicyFound != tt.wantPolicyFound {
				t.Errorf("PolicyFound = %v, want %v", sec.PolicyFound, tt.wantPolicyFound)
			}
			if sec.Scoped != tt.wantScoped {
				t.Errorf("Scoped = %v, want %v", sec.Scoped, tt.wantScoped)
			}
			if (sec.Error != "") != tt.wantErrorSet {
				t.Errorf("Error = %q, wantSet %v", sec.Error, tt.wantErrorSet)
			}
		})
	}
}

func TestMeshSectionState(t *testing.T) {
	tests := []struct {
		effective string
		want      State
	}{
		{MeshStrict, StateOK},
		{MeshPermissive, StateWarning},
		{MeshDisabled, StateDegraded},
		{MeshNone, StateSuspended},
	}
	for _, tt := range tests {
		sec := MeshSection{Effective: tt.effective}
		if got := sec.State(); got != tt.want {
			t.Errorf("State() for %q = %v, want %v", tt.effective, got, tt.want)
		}
	}
}

func peerAuth(namespace string, selector map[string]string, mode string) kube.PeerAuthentication {
	return kube.PeerAuthentication{
		Metadata: kube.PeerAuthenticationMetadata{Namespace: namespace},
		Spec: kube.PeerAuthenticationSpec{
			MTLS:     kube.PeerAuthenticationMTLS{Mode: mode},
			Selector: kube.PeerAuthenticationSelector{MatchLabels: selector},
		},
	}
}

func TestResolveServicePolicyWorkloadWinsOverNamespace(t *testing.T) {
	list := &kube.PeerAuthenticationList{Items: []kube.PeerAuthentication{
		peerAuth("payments", nil, "PERMISSIVE"),                              // namespace-wide
		peerAuth("payments", map[string]string{"app": "billing"}, "DISABLE"), // workload-specific
	}}
	got := ResolveServicePolicy(list, "istio-system", "payments", map[string]string{"app": "billing"}, MeshStrict)
	if got.Effective != MeshDisabled || got.Scope != ScopeWorkload {
		t.Errorf("got %+v, want {disabled workload}", got)
	}
}

func TestResolveServicePolicyNamespaceWinsOverMesh(t *testing.T) {
	list := &kube.PeerAuthenticationList{Items: []kube.PeerAuthentication{
		peerAuth("payments", nil, "PERMISSIVE"),
	}}
	got := ResolveServicePolicy(list, "istio-system", "payments", map[string]string{"app": "billing"}, MeshStrict)
	if got.Effective != MeshPermissive || got.Scope != ScopeNamespace {
		t.Errorf("got %+v, want {permissive namespace}", got)
	}
}

func TestResolveServicePolicyUnsetFallsThroughEachLevel(t *testing.T) {
	// Workload-scoped UNSET falls through to namespace-wide, not to mesh directly.
	list := &kube.PeerAuthenticationList{Items: []kube.PeerAuthentication{
		peerAuth("payments", nil, "DISABLE"),
		peerAuth("payments", map[string]string{"app": "billing"}, "UNSET"),
	}}
	got := ResolveServicePolicy(list, "istio-system", "payments", map[string]string{"app": "billing"}, MeshStrict)
	if got.Effective != MeshDisabled || got.Scope != ScopeNamespace {
		t.Errorf("workload UNSET should fall through to namespace-wide: got %+v", got)
	}

	// Namespace-wide UNSET (and no workload match) falls through to mesh.
	list2 := &kube.PeerAuthenticationList{Items: []kube.PeerAuthentication{
		peerAuth("payments", nil, "UNSET"),
	}}
	got2 := ResolveServicePolicy(list2, "istio-system", "payments", map[string]string{"app": "billing"}, MeshStrict)
	if got2.Effective != MeshStrict || got2.Scope != ScopeMesh {
		t.Errorf("namespace UNSET should fall through to mesh: got %+v", got2)
	}
}

func TestResolveServicePolicyMultipleWorkloadMatchesPicksFirstByListOrder(t *testing.T) {
	// Istio's own behaviour when two workload-scoped policies in one namespace both
	// select the same workload is undefined. This only asserts the deterministic,
	// arbitrary choice this package makes -- first match in list order -- not that it
	// is somehow "correct", since Istio itself defines no correct answer here.
	list := &kube.PeerAuthenticationList{Items: []kube.PeerAuthentication{
		peerAuth("payments", map[string]string{"app": "billing"}, "STRICT"),
		peerAuth("payments", map[string]string{"app": "billing"}, "DISABLE"),
	}}
	got := ResolveServicePolicy(list, "istio-system", "payments", map[string]string{"app": "billing"}, MeshPermissive)
	if got.Effective != MeshStrict || got.Scope != ScopeWorkload {
		t.Errorf("got %+v, want the first matching item (strict), not the second", got)
	}
}

func TestResolveServicePolicyNoOverrideInheritsMeshEffective(t *testing.T) {
	list := &kube.PeerAuthenticationList{Items: []kube.PeerAuthentication{
		peerAuth("other-namespace", nil, "DISABLE"),
	}}
	got := ResolveServicePolicy(list, "istio-system", "payments", map[string]string{"app": "billing"}, MeshStrict)
	if got.Effective != MeshStrict || got.Scope != ScopeMesh {
		t.Errorf("got %+v, want {strict mesh}", got)
	}
}

func TestResolveServicePolicyNilListInheritsMesh(t *testing.T) {
	got := ResolveServicePolicy(nil, "istio-system", "payments", nil, MeshPermissive)
	if got.Effective != MeshPermissive || got.Scope != ScopeMesh {
		t.Errorf("got %+v, want {permissive mesh}", got)
	}
}

func TestResolveServicePolicyMeshNamespaceItselfIsAlwaysMeshScoped(t *testing.T) {
	// Istio ignores a selector on a PeerAuthentication placed in the root namespace,
	// so a workload living there (istiod itself, an ingress gateway) has no
	// namespace- or workload-level override to look for.
	list := &kube.PeerAuthenticationList{Items: []kube.PeerAuthentication{
		peerAuth("istio-system", map[string]string{"app": "istiod"}, "DISABLE"),
	}}
	got := ResolveServicePolicy(list, "istio-system", "istio-system", map[string]string{"app": "istiod"}, MeshStrict)
	if got.Effective != MeshStrict || got.Scope != ScopeMesh {
		t.Errorf("got %+v, want {strict mesh}", got)
	}
}
