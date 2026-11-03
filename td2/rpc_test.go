package tenderduty

import (
	"errors"
	"testing"
	"time"
)

func TestPickNode(t *testing.T) {
	now := time.Now()
	fresh := func(h int64) probeResult { return probeResult{node: &NodeConfig{}, height: h} }
	failed := func(h int64) probeResult {
		return probeResult{node: &NodeConfig{wsFailedAt: now.Add(-time.Minute)}, height: h}
	}
	down := func() probeResult { return probeResult{node: &NodeConfig{}, err: errors.New("down")} }

	cases := []struct {
		name    string
		results []probeResult
		want    int
	}{
		{"highest height wins", []probeResult{fresh(100), fresh(105), fresh(103)}, 1},
		{"ties go to config order", []probeResult{fresh(100), fresh(100)}, 0},
		{"down nodes are skipped", []probeResult{down(), fresh(90)}, 1},
		{"lagging nodes are skipped", []probeResult{fresh(100), fresh(50)}, 0},
		{"recent websocket failure is passed over", []probeResult{failed(100), fresh(99)}, 1},
		{"penalised node is used when it is the only one", []probeResult{failed(100), down()}, 0},
		{"old websocket failure no longer counts", []probeResult{
			{node: &NodeConfig{wsFailedAt: now.Add(-time.Hour)}, height: 100}, fresh(99)}, 0},
		{"nothing usable", []probeResult{down(), down()}, -1},
	}
	for _, c := range cases {
		if got := pickNode(c.results, 10, now); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
