package main

import (
	"testing"

	"github.com/ntmggr/k8s-status/internal/status"
)

func TestNodeStatsDefaultsToOff(t *testing.T) {
	if envBool("NODE_STATS", false) {
		t.Error("NODE_STATS must default to false: the default RBAC has no ClusterRole")
	}
	for _, v := range []string{"true", "1", "TRUE"} {
		t.Setenv("NODE_STATS", v)
		if !envBool("NODE_STATS", false) {
			t.Errorf("NODE_STATS=%q should enable node stats", v)
		}
	}
	for _, v := range []string{"", "  ", "maybe"} {
		t.Setenv("NODE_STATS", v)
		if envBool("NODE_STATS", false) {
			t.Errorf("NODE_STATS=%q should keep the default", v)
		}
	}
}

func TestUnmanagedDefaultsToOff(t *testing.T) {
	if envBool("UNMANAGED", false) {
		t.Error("UNMANAGED must default to false: listing workloads cluster-wide needs a ClusterRole")
	}
	for _, v := range []string{"true", "1", "TRUE"} {
		t.Setenv("UNMANAGED", v)
		if !envBool("UNMANAGED", false) {
			t.Errorf("UNMANAGED=%q should enable the section", v)
		}
	}
	for _, v := range []string{"", "  ", "maybe"} {
		t.Setenv("UNMANAGED", v)
		if envBool("UNMANAGED", false) {
			t.Errorf("UNMANAGED=%q should keep the default", v)
		}
	}
}

func TestUnmanagedIgnoreNamespacesParsing(t *testing.T) {
	if got := splitGlobs(""); got != nil {
		t.Errorf("empty UNMANAGED_IGNORE_NS = %v, want nil", got)
	}
	got := splitGlobs(" kube-* , istio-system ,, ")
	want := []string{"kube-*", "istio-system"}
	if len(got) != len(want) {
		t.Fatalf("globs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("globs = %v, want %v", got, want)
		}
	}
}

func TestSourcesParsing(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []status.Source
		auto bool
	}{
		{"default", "argocd", []status.Source{status.SourceArgoCD}, false},
		{"flux only", "flux", []status.Source{status.SourceFlux}, false},
		{"both", "argocd,flux", []status.Source{status.SourceArgoCD, status.SourceFlux}, false},
		{"order is preserved", "flux,argocd", []status.Source{status.SourceFlux, status.SourceArgoCD}, false},
		{"spacing and case", " ArgoCD , FLUX ", []status.Source{status.SourceArgoCD, status.SourceFlux}, false},
		{"duplicates collapse", "flux,flux,argocd", []status.Source{status.SourceFlux, status.SourceArgoCD}, false},
		{"auto", "auto", nil, true},
		{"auto overrides an explicit list", "argocd,auto", nil, true},
		// A typo must not take the page down, and must not silently disable everything.
		{"unknown value falls back", "helmfile", []status.Source{status.SourceArgoCD}, false},
		{"unknown value beside a good one", "flux,helmfile", []status.Source{status.SourceFlux}, false},
		{"empty", "", []status.Source{status.SourceArgoCD}, false},
		{"separators only", " , , ", []status.Source{status.SourceArgoCD}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, auto := parseSources(tc.raw)
			if auto != tc.auto {
				t.Errorf("auto = %t, want %t", auto, tc.auto)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("sources = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("sources = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestSourcesDefaultKeepsFluxOff(t *testing.T) {
	// The default must never call the Flux APIs: the shipped RBAC does not allow it.
	sources, auto := parseSources(env("SOURCES", string(status.SourceArgoCD)))
	if auto {
		t.Fatal("SOURCES must not default to auto")
	}
	if hasSource(sources, status.SourceFlux) {
		t.Error("flux must be off unless SOURCES asks for it")
	}
	if !hasSource(sources, status.SourceArgoCD) {
		t.Error("argocd must be on by default")
	}
}
