package main

import "testing"

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
