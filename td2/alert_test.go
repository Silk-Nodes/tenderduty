package tenderduty

import "testing"

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
