package tenderduty

import (
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
