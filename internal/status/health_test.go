package status

import "testing"

func TestSummaryHealth(t *testing.T) {
	cases := []struct {
		name string
		sum  Summary
		want Health
	}{
		{
			name: "all services OK",
			sum:  Summary{Total: 5, OK: 5},
			want: Health{Percent: 100, Known: true, Counted: 5, OK: 5, Excluded: 0, Attention: 0},
		},
		{
			name: "every service pruned",
			sum:  Summary{Total: 4, Prune: 4},
			want: Health{Percent: 0, Known: false, Counted: 0, OK: 0, Excluded: 4, Attention: 0},
		},
		{
			name: "suspended only",
			sum:  Summary{Total: 3, Suspended: 3},
			want: Health{Percent: 0, Known: false, Counted: 0, OK: 0, Excluded: 3, Attention: 0},
		},
		{
			name: "rounding boundary",
			sum:  Summary{Total: 3, OK: 1},
			want: Health{Percent: 33, Known: true, Counted: 3, OK: 1, Excluded: 0, Attention: 0},
		},
		{
			name: "defensive clamp when OK exceeds Counted",
			// Should never happen from addService, but must not panic or produce a
			// percentage outside [0,100] if the counters ever disagree.
			sum:  Summary{Total: 3, OK: 5},
			want: Health{Percent: 100, Known: true, Counted: 3, OK: 5, Excluded: 0, Attention: 0},
		},
		{
			name: "counts attention and excluded together",
			sum:  Summary{Total: 14, OK: 6, Degraded: 2, Warning: 1, Progressing: 1, Drift: 1, Prune: 2, Suspended: 1},
			want: Health{Percent: 55, Known: true, Counted: 11, OK: 6, Excluded: 3, Attention: 3},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.sum.Health()
			if got != tc.want {
				t.Errorf("Health() = %+v, want %+v", got, tc.want)
			}
			if got.Percent < 0 || got.Percent > 100 {
				t.Errorf("Percent = %d, out of [0,100]", got.Percent)
			}
		})
	}
}

func TestHealthState(t *testing.T) {
	cases := []struct {
		percent int
		want    State
	}{
		{100, StateOK},
		{90, StateOK},
		{89, StateWarning},
		{75, StateWarning},
		{74, StateDegraded},
		{0, StateDegraded},
	}
	for _, tc := range cases {
		h := Health{Percent: tc.percent}
		if got := h.State(); got != tc.want {
			t.Errorf("Health{Percent: %d}.State() = %v, want %v", tc.percent, got, tc.want)
		}
	}
}
