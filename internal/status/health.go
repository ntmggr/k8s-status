package status

import "math"

// Health is a single at-a-glance number: what share of the services worth counting
// are actually OK right now. It is derived entirely from Summary, so there is no fill
// function and nothing new to fetch.
type Health struct {
	// Percent is OK as a share of Counted, rounded to the nearest integer and clamped
	// to [0,100]. Meaningless when Known is false.
	Percent int
	// Known is false when Counted is zero (or, defensively, negative): there is
	// nothing to divide by, and the page should render "—" rather than a 0% or 100%
	// that implies a verdict nobody can back up.
	Known bool
	// Counted is Total minus the rows deliberately excluded from the percentage.
	Counted int
	OK      int
	// Excluded is Prune plus Suspended: a cleanup backlog and a deliberate pause are
	// not health problems, so neither counts for or against the percentage.
	Excluded int
	// Attention is Degraded plus Warning, offered for callers that want a second
	// number alongside the percentage rather than computing it themselves.
	Attention int
}

// Health folds the summary into one percentage. Progressing and Drift count as not-OK
// in the numerator on purpose: only OK is OK, so the gauge dips honestly while a
// rollout is underway instead of hiding it behind a generous definition of healthy.
func (s Summary) Health() Health {
	h := Health{
		Counted:   s.Total - s.Prune - s.Suspended,
		OK:        s.OK,
		Excluded:  s.Prune + s.Suspended,
		Attention: s.Degraded + s.Warning,
	}
	if h.Counted <= 0 {
		// Every service pruned or suspended (or, defensively, malformed input that
		// makes the denominator negative) leaves nothing to compute a share of.
		return h
	}
	h.Known = true
	pct := int(math.Round(float64(h.OK) * 100 / float64(h.Counted)))
	switch {
	case pct < 0:
		pct = 0
	case pct > 100:
		pct = 100
	}
	h.Percent = pct
	return h
}

// State maps the percentage onto the page's existing colour states, for the gauge's
// colour only: the numeral itself is always shown, never color alone.
func (h Health) State() State {
	switch {
	case h.Percent >= 90:
		return StateOK
	case h.Percent >= 75:
		return StateWarning
	default:
		return StateDegraded
	}
}
