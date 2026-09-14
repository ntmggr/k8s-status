package status

import (
	"strconv"
	"strings"

	"github.com/ntmggr/k8s-status/internal/kube"
)

// parseCPUCores reads a Kubernetes CPU quantity ("2", "3800m", "500000n") as whole
// cores. CPU quantities only ever use the decimal SI suffixes (n/u/m) or none at
// all -- never the binary Ki/Mi/Gi memory uses -- so this only handles those.
func parseCPUCores(q kube.Quantity) float64 {
	s := strings.TrimSpace(string(q))
	if s == "" {
		return 0
	}
	div := 1.0
	switch {
	case strings.HasSuffix(s, "n"):
		s, div = s[:len(s)-1], 1e9
	case strings.HasSuffix(s, "u"):
		s, div = s[:len(s)-1], 1e6
	case strings.HasSuffix(s, "m"):
		s, div = s[:len(s)-1], 1e3
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v / div
}

// memUnits maps every suffix a memory quantity can carry to its byte multiplier.
// Longest suffixes first so "Ki" is not shadowed by a "K" that never appears in the
// Kubernetes API but would otherwise wrongly match as a prefix of "Ki".
var memUnits = []struct {
	suffix string
	mult   float64
}{
	{"Ei", 1 << 60}, {"Pi", 1 << 50}, {"Ti", 1 << 40}, {"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10},
	{"E", 1e18}, {"P", 1e15}, {"T", 1e12}, {"G", 1e9}, {"M", 1e6}, {"k", 1e3},
}

// parseMemoryBytes reads a Kubernetes memory quantity ("16Gi", "16400716Ki",
// "17179869184") as bytes.
func parseMemoryBytes(q kube.Quantity) int64 {
	s := strings.TrimSpace(string(q))
	if s == "" {
		return 0
	}
	mult := 1.0
	for _, u := range memUnits {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = s[:len(s)-len(u.suffix)], u.mult
			break
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0
	}
	return int64(v * mult)
}

// formatCPUCores renders cores the way the rest of the page renders numbers: no
// trailing zeroes, at most one decimal place -- "4" and "3.5", never "3.50".
func formatCPUCores(cores float64) string {
	return strconv.FormatFloat(cores, 'f', -1, 64)
}

// formatMemoryGiB renders bytes as GiB, the unit every cloud provider quotes
// instance memory in, so a node's row reads the same as its own spec sheet.
func formatMemoryGiB(bytes int64) string {
	gib := float64(bytes) / (1 << 30)
	return strconv.FormatFloat(gib, 'f', 1, 64)
}
