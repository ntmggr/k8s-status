package status

import "strings"

// defaultSidecarImages are images that appear alongside a service but are not the
// service itself: the istio proxy, shared auth sidecars, and init helpers. They are
// skipped when deciding which image carries the component version.
var defaultSidecarImages = []string{
	"proxyv2",
	"authz-sidecar",
	"aws-cli",
	"busybox",
	"kubectl",
	"cloudflared",
}

type imageRef struct {
	Full string
	Repo string
	Name string
	Tag  string
}

// parseImage splits a container image reference into its parts. A digest is kept
// in Full but never treated as a tag, so "repo@sha256:..." yields an empty Tag.
func parseImage(ref string) imageRef {
	out := imageRef{Full: ref}
	base := ref
	if i := strings.Index(base, "@"); i >= 0 {
		base = base[:i]
	}
	if i := strings.LastIndex(base, ":"); i >= 0 && !strings.Contains(base[i+1:], "/") {
		out.Repo, out.Tag = base[:i], base[i+1:]
	} else {
		out.Repo = base
	}
	out.Name = out.Repo
	if i := strings.LastIndex(out.Name, "/"); i >= 0 {
		out.Name = out.Name[i+1:]
	}
	return out
}

// componentImage picks the image that represents the service itself.
//
// ArgoCD reports every image in the application, so a row would otherwise show the
// istio sidecar version. Preference order: an exact name match, then a partial match
// in either direction (autoc-api ships autocorrect-api), then the first non-sidecar
// image. Returns the zero value when nothing qualifies.
func componentImage(appName string, images []string, sidecars []string) imageRef {
	deny := make(map[string]bool, len(sidecars))
	for _, s := range sidecars {
		if s = strings.TrimSpace(s); s != "" {
			deny[s] = true
		}
	}

	candidates := make([]imageRef, 0, len(images))
	for _, raw := range images {
		img := parseImage(raw)
		if img.Name == "" || deny[img.Name] {
			continue
		}
		candidates = append(candidates, img)
	}
	if len(candidates) == 0 {
		return imageRef{}
	}

	for _, c := range candidates {
		if c.Name == appName {
			return c
		}
	}
	for _, c := range candidates {
		if strings.Contains(appName, c.Name) || strings.Contains(c.Name, appName) {
			return c
		}
	}
	return candidates[0]
}
