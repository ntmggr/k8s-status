package status

import (
	"testing"

	"github.com/ntmggr/k8s-status/internal/kube"
)

func TestParseCPUCores(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"", 0}, {"4", 4}, {"3800m", 3.8}, {"500000n", 0.0005}, {"2500000u", 2.5}, {"not-a-number", 0}, {"-1", 0},
	}
	for _, tc := range cases {
		if got := parseCPUCores(kube.Quantity(tc.in)); got != tc.want {
			t.Errorf("parseCPUCores(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseMemoryBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0}, {"1024", 1024}, {"1Ki", 1024}, {"16Gi", 16 * (1 << 30)}, {"1M", 1e6}, {"garbage", 0}, {"-5Gi", 0},
	}
	for _, tc := range cases {
		if got := parseMemoryBytes(kube.Quantity(tc.in)); got != tc.want {
			t.Errorf("parseMemoryBytes(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFormatCPUCoresNoTrailingZeroes(t *testing.T) {
	if got := formatCPUCores(4); got != "4" {
		t.Errorf("formatCPUCores(4) = %q, want %q", got, "4")
	}
	if got := formatCPUCores(3.8); got != "3.8" {
		t.Errorf("formatCPUCores(3.8) = %q, want %q", got, "3.8")
	}
}

func TestFormatMemoryGiBOneDecimal(t *testing.T) {
	if got := formatMemoryGiB(16 * (1 << 30)); got != "16.0" {
		t.Errorf("formatMemoryGiB(16Gi) = %q, want %q", got, "16.0")
	}
}
