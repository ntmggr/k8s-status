package web

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ntmggr/k8s-status/internal/status"
)

// Twice now a field was added to the JSON response and the line that assigns it was
// left out, so the API served a zero while the page showed the real number. Nothing
// failed: the struct compiled, the tests passed, and only reading the JSON revealed it.
//
// This fills every int on the source with a distinct non-zero value and asserts that
// none of them arrives as zero. Adding a field without wiring it now fails here.
func TestEveryNumericSummaryFieldIsMapped(t *testing.T) {
	var sum status.Summary
	fill(reflect.ValueOf(&sum).Elem())

	snap := &status.Snapshot{Summary: sum}
	body := marshalSummary(t, snap)

	for k, v := range body {
		if n, ok := v.(float64); ok && n == 0 {
			t.Errorf("summary field %q serialised as 0, so it is probably never assigned", k)
		}
	}
}

func TestEveryNumericNodeFieldIsMapped(t *testing.T) {
	var ns status.NodeStats
	fill(reflect.ValueOf(&ns).Elem())
	// nodesJSON draws from the summary as well, so both sides have to be non-zero or
	// a correctly mapped field looks unmapped.
	var sum status.Summary
	fill(reflect.ValueOf(&sum).Elem())

	snap := &status.Snapshot{Nodes: &ns, Summary: sum}
	out := nodes(snap)
	if out == nil {
		t.Fatal("no node payload")
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range m {
		if n, ok := v.(float64); ok && n == 0 {
			t.Errorf("nodes field %q serialised as 0, so it is probably never assigned", k)
		}
	}
}

// fill sets every int field to a distinct non-zero value.
func fill(v reflect.Value) {
	seed := 1
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.Int && f.CanSet() {
			seed++
			f.SetInt(int64(seed))
		}
	}
}

func marshalSummary(t *testing.T, snap *status.Snapshot) map[string]any {
	t.Helper()
	raw, err := json.Marshal(summaryOf(snap))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
