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

func TestMeshPolicyDecodesModeNamespaceAndSelector(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := peerAuthenticationPath(IstioSecurityGroupVersion, IstioSystemNamespace)
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"metadata":{"name":"default","namespace":"istio-system"},"spec":{"mtls":{"mode":"STRICT"},"selector":{"matchLabels":{"app":"payments"}}}}`))
	}))

	pa, err := c.MeshPolicy(context.Background(), IstioSecurityGroupVersion, IstioSystemNamespace)
	if err != nil {
		t.Fatalf("MeshPolicy: %v", err)
	}
	if pa.Spec.MTLS.Mode != "STRICT" {
		t.Errorf("mode = %q, want STRICT", pa.Spec.MTLS.Mode)
	}
	if pa.Metadata.Namespace != "istio-system" || pa.Metadata.Name != "default" {
		t.Errorf("metadata = %+v, want namespace istio-system, name default", pa.Metadata)
	}
	if len(pa.Spec.Selector.MatchLabels) == 0 {
		t.Error("selector should be non-empty when the object carries one")
	}
	if pa.Spec.Selector.MatchLabels["app"] != "payments" {
		t.Errorf("selector matchLabels = %+v, want app=payments", pa.Spec.Selector.MatchLabels)
	}
}

func TestListPeerAuthenticationsReadsClusterWideCollection(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/apis/" + IstioSecurityGroupVersion + "/peerauthentications"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"metadata":{"name":"default","namespace":"istio-system"},"spec":{"mtls":{"mode":"STRICT"}}},
			{"metadata":{"name":"ns-wide","namespace":"payments"},"spec":{"mtls":{"mode":"PERMISSIVE"}}},
			{"metadata":{"name":"workload","namespace":"payments"},"spec":{"mtls":{"mode":"DISABLE"},"selector":{"matchLabels":{"app":"payments"}}}}
		]}`))
	}))

	list, err := c.ListPeerAuthentications(context.Background(), IstioSecurityGroupVersion)
	if err != nil {
		t.Fatalf("ListPeerAuthentications: %v", err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(list.Items))
	}
	if list.Items[1].Metadata.Namespace != "payments" || len(list.Items[1].Spec.Selector.MatchLabels) != 0 {
		t.Errorf("namespace-wide item = %+v", list.Items[1])
	}
	if list.Items[2].Spec.Selector.MatchLabels["app"] != "payments" {
		t.Errorf("workload-scoped item = %+v", list.Items[2])
	}
}

func TestListPeerAuthenticationsPropagatesDenied(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))

	list, err := c.ListPeerAuthentications(context.Background(), IstioSecurityGroupVersion)
	if list != nil {
		t.Errorf("list = %+v, want nil", list)
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Fatalf("err = %v, want a 403 StatusError", err)
	}
}
