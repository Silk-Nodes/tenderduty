package tenderduty

import (
	"encoding/json"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"strings"
	"testing"
	"time"
)

func TestAlertFormatting(t *testing.T) {
	msg := &alertMsg{chain: "Osmosis", chainId: "osmosis-1", severity: "info",
		message: "proposal #1058 (IBC Rate Limiting v3 - Upgrade contract) on osmosis-1 closes in under 24 hours (2026-11-04 14:28 UTC) and artifact has still not voted"}
	if got := alertHeading(msg); got != "🗳️ Governance on Osmosis" {
		t.Errorf("heading = %q", got)
	}
	want := "proposal #1058 (IBC Rate Limiting v3 - Upgrade contract)\ncloses in under 24 hours (2026-11-04 14:28 UTC)\nartifact has still not voted"
	if got := alertBody(msg); got != want {
		t.Errorf("body = %q", got)
	}
	msg.resolved = true
	if got := alertHeading(msg); got != "✅ Resolved: Governance on Osmosis" {
		t.Errorf("resolved heading = %q", got)
	}
	for text, kind := range map[string]string{
		"upgrade v29 is scheduled at height 1 on x":             "Upgrade",
		"upgrade proposal #3 (t) on x is open":                  "Governance",
		"RPC node http://a has been down for > 2 minutes on x":  "RPC node down",
		"artifact has missed 5 blocks on x":                     "Missed blocks",
		"stalled: have not seen a new block on x in 10 minutes": "Chain stalled",
		"something else": "Alert",
	} {
		if _, k := alertKind(text); k != kind {
			t.Errorf("%q classified as %s, want %s", text, k, kind)
		}
	}
}

func TestListAlarmsOrder(t *testing.T) {
	alarms.notifyMux.Lock()
	alarms.AllAlarms["t"] = map[string]alarmInfo{
		"proposal #1059 (x) on c is open for voting until 2026-11-06 22:08 UTC and v has not voted":         {Since: time.Unix(100, 0)},
		"proposal #1057 (y) on c closes in under 24 hours (2026-11-04 14:26 UTC) and v has still not voted": {Since: time.Unix(100, 0)},
		"proposal #1058 (z) on c is open for voting until 2026-11-04 14:28 UTC and v has not voted":         {Since: time.Unix(100, 0)},
		"RPC node http://a has been down for > 20 minutes on c":                                             {Since: time.Unix(200, 0)},
	}
	alarms.notifyMux.Unlock()
	got := listAlarms("t")
	want := []string{"RPC node", "proposal #1057", "proposal #1058", "proposal #1059"}
	for i, w := range want {
		if len(got[i].Message) < len(w) || got[i].Message[:len(w)] != w {
			t.Errorf("position %d = %q, want prefix %q", i, got[i].Message, w)
		}
	}
}

func TestAlarmInfoReadsOldState(t *testing.T) {
	var m map[string]alarmInfo
	if err := json.Unmarshal([]byte(`{"old":"2026-11-05T10:14:10Z","new":{"since":"2026-11-06T15:34:00Z","severity":"warning"}}`), &m); err != nil {
		t.Fatal(err)
	}
	if m["old"].Since.Year() != 2026 || m["old"].Severity != "" || m["new"].Severity != "warning" {
		t.Fatalf("%+v", m)
	}
}

func TestAlarmCollector(t *testing.T) {
	cc := &ChainConfig{name: "c", ChainId: "c-1", valInfo: &ValInfo{Moniker: "val"}}
	saved := td
	td = &Config{Chains: map[string]*ChainConfig{"c": cc}}
	defer func() { td = saved }()
	alarms.notifyMux.Lock()
	alarms.AllAlarms["c"] = map[string]alarmInfo{"upgrade v2 is scheduled at height 10 on c-1": {Since: time.Unix(1, 0), Severity: "info"}}
	alarms.notifyMux.Unlock()
	defer alarms.forget("c", "upgrade v2 is scheduled at height 10 on c-1")
	ch := make(chan prometheus.Metric, 10)
	newAlarmCollector().Collect(ch)
	close(ch)
	var got []string
	for m := range ch {
		d := &dto.Metric{}
		if err := m.Write(d); err != nil {
			t.Fatal(err)
		}
		for _, lp := range d.Label {
			got = append(got, lp.GetName()+"="+lp.GetValue())
		}
		if d.Gauge.GetValue() != 1 {
			t.Fatalf("value %v", d.Gauge.GetValue())
		}
	}
	want := "chain_id=c-1 kind=Upgrade message=upgrade v2 is scheduled at height 10 on c-1 moniker=val name=c severity=info"
	if strings.Join(got, " ") != want {
		t.Fatalf("labels %q", strings.Join(got, " "))
	}
}
