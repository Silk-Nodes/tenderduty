package tenderduty

import (
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
	alarms.AllAlarms["t"] = map[string]time.Time{
		"proposal #1059 (x) on c is open for voting until 2026-11-06 22:08 UTC and v has not voted":         time.Unix(100, 0),
		"proposal #1057 (y) on c closes in under 24 hours (2026-11-04 14:26 UTC) and v has still not voted": time.Unix(100, 0),
		"proposal #1058 (z) on c is open for voting until 2026-11-04 14:28 UTC and v has not voted":         time.Unix(100, 0),
		"RPC node http://a has been down for > 20 minutes on c":                                             time.Unix(200, 0),
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
