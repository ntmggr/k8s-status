package status

import "testing"

func TestComponentImage(t *testing.T) {
	sidecars := defaultSidecarImages
	cases := []struct {
		name    string
		app     string
		images  []string
		wantTag string
		wantNm  string
	}{
		{
			name:    "skips the istio sidecar",
			app:     "agi-connector",
			images:  []string{"reg.invalid/agi-connector:4.24.0", "registry.istio.io/release/proxyv2:1.30.1"},
			wantTag: "4.24.0", wantNm: "agi-connector",
		},
		{
			name:    "partial match when image name differs from app name",
			app:     "autoc-api",
			images:  []string{"reg.invalid/proxyv2:1.0", "reg.invalid/autocorrect-api:1.16.0"},
			wantTag: "1.16.0", wantNm: "autocorrect-api",
		},
		{
			name:    "app name contained in image name",
			app:     "agent-vb-middleware",
			images:  []string{"reg.invalid/agent-vb-mw:6.21.0"},
			wantTag: "6.21.0", wantNm: "agent-vb-mw",
		},
		{
			name:    "falls back to first non-sidecar when nothing matches",
			app:     "ml-nlu-encoder-minilm",
			images:  []string{"reg.invalid/aws-cli:2.0", "reg.invalid/ml-server:3.3.2"},
			wantTag: "3.3.2", wantNm: "ml-server",
		},
		{
			name:   "all sidecars yields nothing",
			app:    "whatever",
			images: []string{"reg.invalid/proxyv2:1.0", "reg.invalid/busybox:1.36"},
		},
		{
			name:   "no images yields nothing",
			app:    "whatever",
			images: nil,
		},
		{
			name:    "digest reference has no tag but still resolves the name",
			app:     "orders-api",
			images:  []string{"reg.invalid/orders-api@sha256:abc123"},
			wantTag: "", wantNm: "orders-api",
		},
		{
			name:    "registry with a port is not mistaken for a tag",
			app:     "orders-api",
			images:  []string{"reg.invalid:5000/orders-api:2.1.0"},
			wantTag: "2.1.0", wantNm: "orders-api",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := componentImage(c.app, c.images, sidecars)
			if got.Tag != c.wantTag || got.Name != c.wantNm {
				t.Errorf("componentImage(%q) = name %q tag %q, want name %q tag %q",
					c.app, got.Name, got.Tag, c.wantNm, c.wantTag)
			}
		})
	}
}

func TestParseImageRegistryPort(t *testing.T) {
	got := parseImage("reg.invalid:5000/group/app:1.2.3")
	if got.Repo != "reg.invalid:5000/group/app" || got.Tag != "1.2.3" || got.Name != "app" {
		t.Errorf("parseImage = %+v", got)
	}
}
