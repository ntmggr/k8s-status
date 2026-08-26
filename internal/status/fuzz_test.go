package status

import (
	"strings"
	"testing"
)

// FuzzParseImage exercises the image reference parser, which is the one place this
// project pulls structure out of a free-form string that comes from the cluster. A
// registry with a port, a digest, no tag and a bare name all reach it, so the parse
// must not panic or invent a tag that is not in the input.
func FuzzParseImage(f *testing.F) {
	for _, seed := range []string{
		"nginx",
		"nginx:1.25",
		"docker.io/library/nginx:1.25",
		"registry.example.com:5000/team/app:v1.2.3",
		"app@sha256:" + strings.Repeat("a", 64),
		"registry.example.com:5000/team/app@sha256:" + strings.Repeat("b", 64),
		"",
		":",
		"@",
		"a:b:c",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, ref string) {
		got := parseImage(ref)

		if got.Full != ref {
			t.Fatalf("Full=%q, want the input %q unchanged", got.Full, ref)
		}
		// A tag has to be a substring of the input: anything else means the parser
		// fabricated it.
		if got.Tag != "" && !strings.Contains(ref, got.Tag) {
			t.Fatalf("Tag=%q is not present in input %q", got.Tag, ref)
		}
		// A tag never contains a separator; that would mean a registry port or a
		// path segment leaked into it.
		if strings.ContainsAny(got.Tag, "/@") {
			t.Fatalf("Tag=%q contains a separator", got.Tag)
		}
	})
}
