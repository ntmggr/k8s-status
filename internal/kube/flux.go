package kube

import (
	"context"
	"errors"
	"sync"
)

// Flux serves each controller's objects under its own group. These are the current
// stable versions: helm-controller on v2, kustomize-controller on v1.
const (
	HelmReleaseGroupVersion   = "helm.toolkit.fluxcd.io/v2"
	KustomizationGroupVersion = "kustomize.toolkit.fluxcd.io/v1"

	ResourceHelmReleases   = "helmreleases"
	ResourceKustomizations = "kustomizations"
)

// Cluster-wide collection endpoints. Unlike ArgoCD Applications, which all live in one
// namespace, Flux objects are spread across the namespaces they deploy into, so one
// call per kind covers the cluster and a namespaced Role cannot serve them.
const (
	helmReleasesPath   = "/apis/" + HelmReleaseGroupVersion + "/" + ResourceHelmReleases
	kustomizationsPath = "/apis/" + KustomizationGroupVersion + "/" + ResourceKustomizations
)

const (
	KindHelmRelease   = "HelmRelease"
	KindKustomization = "Kustomization"
)

// FluxMetadata decodes only the identifying fields of a Flux object.
type FluxMetadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// Condition is one entry of status.conditions. Flux uses the standard metav1.Condition
// shape, so Status is the string "True", "False" or "Unknown" rather than a boolean.
type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type HelmReleaseList struct {
	Items []HelmRelease `json:"items"`
}

type HelmRelease struct {
	Metadata FluxMetadata      `json:"metadata"`
	Spec     HelmReleaseSpec   `json:"spec"`
	Status   HelmReleaseStatus `json:"status"`
}

type HelmReleaseSpec struct {
	Suspend bool              `json:"suspend"`
	Chart   HelmChartTemplate `json:"chart"`
}

type HelmChartTemplate struct {
	Spec HelmChartSpec `json:"spec"`
}

type HelmChartSpec struct {
	Chart   string `json:"chart"`
	Version string `json:"version"`
}

// HelmReleaseStatus carries chart versions, never a git revision: helm-controller
// resolves a chart before it installs, so what it records is the chart it used.
type HelmReleaseStatus struct {
	Conditions []Condition `json:"conditions"`
	// LastAttemptedRevision is the chart version of the most recent attempt, which may
	// have failed. helm.toolkit.fluxcd.io/v2 has no lastAppliedRevision at all.
	LastAttemptedRevision string `json:"lastAttemptedRevision"`
	// LastAppliedRevision only exists on helm-controller v0.x and is read for clusters
	// still serving the older API.
	LastAppliedRevision string               `json:"lastAppliedRevision"`
	History             []HelmReleaseHistory `json:"history"`
}

// HelmReleaseHistory is newest-first; entry 0 is the release currently installed.
// AppVersion is the application's own version as the chart declares it, and is not the
// same thing as ChartVersion.
type HelmReleaseHistory struct {
	ChartVersion string `json:"chartVersion"`
	ChartName    string `json:"chartName"`
	AppVersion   string `json:"appVersion"`
	LastDeployed string `json:"lastDeployed"`
}

type KustomizationList struct {
	Items []Kustomization `json:"items"`
}

type Kustomization struct {
	Metadata FluxMetadata        `json:"metadata"`
	Spec     KustomizationSpec   `json:"spec"`
	Status   KustomizationStatus `json:"status"`
}

type KustomizationSpec struct {
	Suspend bool   `json:"suspend"`
	Path    string `json:"path"`
}

type KustomizationStatus struct {
	Conditions []Condition `json:"conditions"`
	// LastAppliedRevision is the source revision, written as "<ref>@<algo>:<digest>".
	LastAppliedRevision string `json:"lastAppliedRevision"`
}

// FluxList is one read of both Flux kinds.
type FluxList struct {
	HelmReleases   []HelmRelease
	Kustomizations []Kustomization
}

// ListFlux returns every HelmRelease and Kustomization in the cluster.
//
// The two kinds are fetched concurrently under one timeout, and a kind that fails does
// not discard the other, mirroring ListWorkloads: a cluster running only
// kustomize-controller still gets its rows, with a note about the half that failed.
func (c *Client) ListFlux(ctx context.Context) (*FluxList, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	var (
		out  FluxList
		mu   sync.Mutex
		errs = make([]error, 2)
		wg   sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		var list HelmReleaseList
		if err := c.GetJSON(ctx, FromCache(helmReleasesPath), "helmrelease list", &list); err != nil {
			errs[0] = err
			return
		}
		mu.Lock()
		out.HelmReleases = list.Items
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		var list KustomizationList
		if err := c.GetJSON(ctx, FromCache(kustomizationsPath), "kustomization list", &list); err != nil {
			errs[1] = err
			return
		}
		mu.Lock()
		out.Kustomizations = list.Items
		mu.Unlock()
	}()
	wg.Wait()

	return &out, errors.Join(errs...)
}
