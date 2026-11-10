package tenderduty

import (
	"strings"
	"testing"
	"time"

	upgrade "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
)

func TestAccountAddress(t *testing.T) {
	bz := make([]byte, 20)
	for i := range bz {
		bz[i] = byte(i)
	}
	cases := map[string]string{"cosmosvaloper": "cosmos", "osmovaloper": "osmo", "iva": "iaa"}
	for operPrefix, accPrefix := range cases {
		oper, _ := bech32.ConvertAndEncode(operPrefix, bz)
		want, _ := bech32.ConvertAndEncode(accPrefix, bz)
		got, err := accountAddress(oper)
		if err != nil || got != want {
			t.Errorf("%s: got %s (%v), want %s", oper, got, err, want)
		}
	}
	if _, err := accountAddress("not an address"); err == nil {
		t.Error("expected an error for garbage input")
	}
}

func TestIsUpgrade(t *testing.T) {
	if !isUpgrade("/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade") || !isUpgrade("/cosmos.upgrade.v1beta1.SoftwareUpgradeProposal") {
		t.Error("upgrade messages not recognised")
	}
	if isUpgrade("/cosmos.gov.v1beta1.TextProposal") || isUpgrade("/cosmos.upgrade.v1beta1.MsgCancelUpgrade") {
		t.Error("non-upgrade message flagged as upgrade")
	}
}

func TestUpgradeEta(t *testing.T) {
	cc := &ChainConfig{lastBlockNum: 1000, avgBlockTime: 6}
	if _, ok := cc.upgradeEta(); ok {
		t.Error("no plan should mean no eta")
	}
	cc.upgrade = &upgrade.Plan{Name: "v2", Height: 1600}
	if eta, ok := cc.upgradeEta(); !ok || eta != time.Hour {
		t.Errorf("600 blocks at 6s should be one hour, got %v %v", eta, ok)
	}
	cc.upgrade.Height = 900
	if eta, ok := cc.upgradeEta(); !ok || eta != 0 {
		t.Errorf("a height already passed should give zero, got %v", eta)
	}
	cc.avgBlockTime = 0
	if _, ok := cc.upgradeEta(); ok {
		t.Error("no block timing should mean no eta")
	}
}

func TestConsensusPrefix(t *testing.T) {
	for hrp, want := range map[string]string{"cosmosvaloper": "cosmosvalcons", "iva": "ica"} {
		if got := consensusPrefix(hrp); got != want {
			t.Errorf("consensusPrefix(%s) = %s, want %s", hrp, got, want)
		}
	}
}

func TestUpgradeHeldUntilChainMoves(t *testing.T) {
	// the plan a node reports and the height of the last block seen decide whether the upgrade is still open
	cases := []struct {
		planGone  bool
		lastBlock int64
		keep      bool
	}{
		{false, 90, true},  // plan still reported: open
		{true, 50, false},  // gone long before the height: cancelled
		{true, 99, true},   // chain halted at height-1: still open
		{true, 100, true},  // first block at the height, not yet past it: still open
		{true, 101, false}, // a block beyond the height: done
	}
	for _, c := range cases {
		held := c.planGone && c.lastBlock >= 100-1 && c.lastBlock <= 100
		open := !c.planGone || held
		if open != c.keep {
			t.Errorf("planGone=%v lastBlock=%d: open=%v, want %v", c.planGone, c.lastBlock, open, c.keep)
		}
	}
}

func TestPlanFromProposalMessage(t *testing.T) {
	msg, _ := (&upgrade.MsgSoftwareUpgrade{Authority: "gov", Plan: upgrade.Plan{Name: "v9", Height: 12345}}).Marshal()
	if name, height := planFromMessage("/cosmos.upgrade.v1beta1.MsgSoftwareUpgrade", msg); name != "v9" || height != 12345 {
		t.Errorf("v1 message: got %q %d", name, height)
	}
	legacy, _ := (&upgrade.SoftwareUpgradeProposal{Title: "t", Plan: upgrade.Plan{Name: "v10", Height: 777}}).Marshal() //nolint:staticcheck
	if name, height := planFromMessage("/cosmos.upgrade.v1beta1.SoftwareUpgradeProposal", legacy); name != "v10" || height != 777 {
		t.Errorf("legacy content: got %q %d", name, height)
	}
	if name, height := planFromMessage("/cosmos.gov.v1beta1.TextProposal", legacy); name != "" || height != 0 {
		t.Errorf("text proposal should give nothing, got %q %d", name, height)
	}
}

// drain returns the alerts queued since the last call.
func drain(ch chan *alertMsg) []*alertMsg {
	var out []*alertMsg
	for {
		select {
		case m := <-ch:
			out = append(out, m)
		default:
			return out
		}
	}
}

func TestUpgradeProposalThenPlan(t *testing.T) {
	cc := &ChainConfig{name: "c", ChainId: "c-1", Alerts: AlertConfig{UpgradeAlerts: true, UpgradeNoticeHours: 24, UpgradeImminentMinutes: 60, UpgradePriority: "info"}}
	cc.valInfo = &ValInfo{}
	saved := td
	td = &Config{Chains: map[string]*ChainConfig{"c": cc}, alertChan: make(chan *alertMsg, 20)}
	defer func() { td = saved }()

	ends := time.Now().Add(48 * time.Hour)
	cc.proposals = []proposalInfo{{ID: 42, Title: "upgrade", VotingEnd: ends, IsUpgrade: true, UpgradeName: "v9", UpgradeHeight: 5000}}
	cc.alertUpgrade()
	got := drain(td.alertChan)
	if len(got) != 1 || got[0].resolved || !strings.HasPrefix(got[0].message, "upgrade v9 proposed on c-1: proposal #42") {
		t.Fatalf("proposal stage: %+v", got)
	}
	cc.alertUpgrade()
	if got = drain(td.alertChan); len(got) != 0 {
		t.Fatalf("repeat refresh sent %d alerts", len(got))
	}
	// the vote passes: the proposal is gone and the plan is scheduled; the scheduled notice takes over, no resolve
	cc.proposals = nil
	cc.upgrade = &upgrade.Plan{Name: "v9", Height: 5000}
	cc.alertUpgrade()
	got = drain(td.alertChan)
	if len(got) != 1 || got[0].resolved || got[0].message != "upgrade v9 is scheduled at height 5000 on c-1" {
		t.Fatalf("plan stage: %+v", got)
	}
	// the upgrade is through: one resolve
	cc.upgrade = nil
	cc.lastProposed = nil
	cc.alertUpgrade()
	got = drain(td.alertChan)
	if len(got) != 1 || !got[0].resolved {
		t.Fatalf("resolve: %+v", got)
	}
}

func TestUpgradeProposalRejected(t *testing.T) {
	cc := &ChainConfig{name: "c", ChainId: "c-1", Alerts: AlertConfig{UpgradeAlerts: true, UpgradeNoticeHours: 24, UpgradeImminentMinutes: 60}}
	cc.valInfo = &ValInfo{}
	saved := td
	td = &Config{Chains: map[string]*ChainConfig{"c": cc}, alertChan: make(chan *alertMsg, 20)}
	defer func() { td = saved }()
	// voting ended an hour ago, no plan followed: the notice is resolved
	cc.proposals = []proposalInfo{{ID: 7, VotingEnd: time.Now().Add(-time.Hour), IsUpgrade: true, UpgradeName: "v2", UpgradeHeight: 10}}
	cc.alertUpgrade()
	drain(td.alertChan)
	cc.proposals = nil
	cc.alertUpgrade()
	got := drain(td.alertChan)
	if len(got) != 1 || !got[0].resolved {
		t.Fatalf("rejected proposal: %+v", got)
	}
}

func TestGovResolveText(t *testing.T) {
	cc := &ChainConfig{name: "c", ChainId: "c-1", operator: "op", Alerts: AlertConfig{GovAlerts: true, GovFinalHours: 24, GovPriority: "info"}}
	cc.valInfo = &ValInfo{Moniker: "val"}
	saved := td
	td = &Config{Chains: map[string]*ChainConfig{"c": cc}, alertChan: make(chan *alertMsg, 20)}
	defer func() { td = saved }()
	ends := time.Now().Add(72 * time.Hour)
	cc.proposals = []proposalInfo{{ID: 41, Title: "t", VotingEnd: ends}, {ID: 42, Title: "u", VotingEnd: ends}}
	cc.alertGov()
	got := drain(td.alertChan)
	if len(got) != 2 || got[0].text != "" {
		t.Fatalf("reminders: %+v", got)
	}
	// 41 is voted on, 42 leaves the voting period without a vote
	cc.proposals = []proposalInfo{{ID: 41, Title: "t", VotingEnd: ends, Voted: true}}
	cc.alertGov()
	got = drain(td.alertChan)
	if len(got) != 2 {
		t.Fatalf("resolves: %+v", got)
	}
	want := map[string]string{
		"proposal #41 (t) on c-1 is open for voting until " + ends.UTC().Format("2006-01-02 15:04 UTC") + " and val has not voted": "val voted on proposal #41 (t) on c-1",
		"proposal #42 (u) on c-1 is open for voting until " + ends.UTC().Format("2006-01-02 15:04 UTC") + " and val has not voted": "voting on proposal #42 (u) on c-1 closed and val did not vote",
	}
	for _, g := range got {
		if !g.resolved || want[g.message] != g.text {
			t.Fatalf("resolve %q got text %q", g.message, g.text)
		}
		if body := alertBody(g); strings.Contains(body, "not voted") {
			t.Fatalf("chat body still reads as a reminder: %q", body)
		}
	}
}
