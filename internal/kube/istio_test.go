package kube

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func discoveryHandler(t *testing.T, resources map[string][]string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		names, ok := resources[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		list := APIResourceList{}
		for _, n := range names {
			list.Resources = append(list.Resources, APIResource{Name: n, Kind: "whatever"})
		}
		_ = json.NewEncoder(w).Encode(list)
	})
}

func TestDetectIstioPrefersV1(t *testing.T) {
	c := newTestClient(t, discoveryHandler(t, map[string][]string{
		"/apis/" + IstioSecurityGroupVersion:     {ResourcePeerAuthentications},
		"/apis/" + IstioSecurityGroupVersionBeta: {ResourcePeerAuthentications},
	}))

	gv, err := c.DetectIstio(context.Background())
	if err != nil {
		t.Fatalf("DetectIstio: %v", err)
	}
	if gv != IstioSecurityGroupVersion {
		t.Errorf("groupVersion = %q, want %q", gv, IstioSecurityGroupVersion)
	}
}

func TestDetectIstioFallsBackToV1Beta1(t *testing.T) {
	c := newTestClient(t, discoveryHandler(t, map[string][]string{
		"/apis/" + IstioSecurityGroupVersionBeta: {ResourcePeerAuthentications},
	}))

	gv, err := c.DetectIstio(context.Background())
	if err != nil {
		t.Fatalf("DetectIstio: %v", err)
	}
	if gv != IstioSecurityGroupVersionBeta {
		t.Errorf("groupVersion = %q, want %q", gv, IstioSecurityGroupVersionBeta)
	}
}

func TestDetectIstioReturnsEmptyWhenNeitherIsServed(t *testing.T) {
	c := newTestClient(t, discoveryHandler(t, map[string][]string{}))

	gv, err := c.DetectIstio(context.Background())
	if err != nil {
		t.Fatalf("DetectIstio: %v, want nil error", err)
	}
	if gv != "" {
		t.Errorf("groupVersion = %q, want empty", gv)
	}
}

func TestMeshPolicyNotFoundIsDistinctFromDiscoveryNotFound(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"peerauthentications.security.istio.io \"default\" not found"}`))
	}))

	pa, err := c.MeshPolicy(context.Background(), IstioSecurityGroupVersion, IstioSystemNamespace)
	if pa != nil {
		t.Errorf("pa = %+v, want nil", pa)
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a *StatusError", err)
	}
	if se.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", se.Code)
	}
}

func TestMeshPolicyDecodesModeAndSelectorPresence(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := peerAuthenticationPath(IstioSecurityGroupVersion, IstioSystemNamespace)
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"spec":{"mtls":{"mode":"STRICT"},"selector":{"matchLabels":{"app":"payments"}}}}`))
	}))

	pa, err := c.MeshPolicy(context.Background(), IstioSecurityGroupVersion, IstioSystemNamespace)
	if err != nil {
		t.Fatalf("MeshPolicy: %v", err)
	}
	if pa.Spec.MTLS.Mode != "STRICT" {
		t.Errorf("mode = %q, want STRICT", pa.Spec.MTLS.Mode)
	}
	if len(pa.Spec.Selector) == 0 {
		t.Error("selector should be non-empty when the object carries one")
	}
}
