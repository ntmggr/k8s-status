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
				Selector: map[string]any{"matchLabels": map[string]any{"app": "payments"}},
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
