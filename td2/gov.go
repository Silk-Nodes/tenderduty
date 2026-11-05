package tenderduty

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	upgrade "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	"github.com/cosmos/cosmos-sdk/types/query"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	govv1beta1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1beta1"
)

// proposalInfo is what the dashboard and alerts need to know about a proposal in its voting period.
type proposalInfo struct {
	ID        uint64    `json:"id"`
	Title     string    `json:"title"`
	VotingEnd time.Time `json:"voting_end"`
	IsUpgrade bool      `json:"is_upgrade"`
	Voted     bool      `json:"voted"`
}

// govAlarm remembers the exact text sent for a proposal so the resolve carries the same text.
type govAlarm struct {
	msg      string
	finalMsg string
}

// prefixOverrides lists the chains whose account and consensus bech32 prefixes are not derived from the
// operator prefix by the usual "<account>valoper" / "<account>valcons" rule.
var prefixOverrides = map[string]struct{ account, consensus string }{
	"iva": {"iaa", "ica"}, // Iris hub
}

// accountPrefix is the account bech32 prefix for an operator prefix.
func accountPrefix(operatorHrp string) string {
	if p, ok := prefixOverrides[operatorHrp]; ok {
		return p.account
	}
	return strings.TrimSuffix(operatorHrp, "valoper")
}

// consensusPrefix is the valcons bech32 prefix for an operator prefix.
func consensusPrefix(operatorHrp string) string {
	if p, ok := prefixOverrides[operatorHrp]; ok {
		return p.consensus
	}
	return strings.TrimSuffix(operatorHrp, "valoper") + "valcons"
}

// accountAddress is the validator's account address: the operator address re-encoded with the account prefix.
func accountAddress(valoper string) (string, error) {
	hrp, bz, err := bech32.DecodeAndConvert(valoper)
	if err != nil {
		return "", err
	}
	return bech32.ConvertAndEncode(accountPrefix(hrp), bz)
}

// isUpgrade reports whether a message or legacy content type URL is a software upgrade.
func isUpgrade(typeUrl string) bool {
	return strings.Contains(typeUrl, "SoftwareUpgrade")
}

// legacyTitle reads the title from any v1beta1 proposal content. Every content type puts the title in field one,
// so decoding as a text proposal works for all of them.
func legacyTitle(value []byte) string {
	t := &govv1beta1.TextProposal{}
	if t.Unmarshal(value) != nil {
		return ""
	}
	return t.Title
}

// shortTitle keeps alert text readable when a proposal title runs long.
func shortTitle(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}

// refreshUpgrade asks x/upgrade for the scheduled plan.
func (cc *ChainConfig) refreshUpgrade(ctx context.Context) error {
	resp := &upgrade.QueryCurrentPlanResponse{}
	if err := abciQuery(ctx, cc.client, "/cosmos.upgrade.v1beta1.Query/CurrentPlan", &upgrade.QueryCurrentPlanRequest{}, resp); err != nil {
		return err
	}
	// a node drops the plan the moment it has applied the upgrade, which can be while the chain is still halted
	// waiting for enough validators. The upgrade only counts as done once a block past its height has been seen,
	// so the plan is kept until then; a plan that vanishes well before its height was cancelled.
	if resp.Plan == nil && cc.upgrade != nil && cc.lastBlockNum >= cc.upgrade.Height-1 && cc.lastBlockNum <= cc.upgrade.Height {
		return nil
	}
	if resp.Plan != nil {
		resp.Plan.Name = clean(resp.Plan.Name, 70)
	}
	cc.upgrade = resp.Plan
	return nil
}

// refreshProposals lists proposals in their voting period and whether the validator voted on each. Chains that
// only answer the older gov v1beta1 queries are detected on the first attempt and remembered.
func (cc *ChainConfig) refreshProposals(ctx context.Context) error {
	voter, err := accountAddress(cc.operator)
	if err != nil {
		return err
	}
	var props []proposalInfo
	if !cc.govLegacy {
		props, err = cc.proposalsV1(ctx)
		if isAbciError(err) {
			if props, err = cc.proposalsV1beta1(ctx); err == nil {
				cc.govLegacy = true
				l(cc.ChainId, "answers gov v1beta1 queries only, using those")
			}
		}
	} else {
		props, err = cc.proposalsV1beta1(ctx)
	}
	if err != nil {
		return err
	}
	for i := range props {
		if props[i].Voted, err = cc.hasVoted(ctx, props[i].ID, voter); err != nil {
			return err
		}
	}
	cc.proposals = props
	return nil
}

// proposalLookback is how many of the most recent proposals are listed when looking for open ones. A full
// listing walks every proposal since genesis, and on long-lived chains the node can no longer decode the oldest
// ones (their message types were removed from the binary) and errors out before reaching the current ones.
const proposalLookback = 50

// recentProposalsPage builds the page request for the last proposalLookback proposals, starting at the key of
// the lowest id in that window. The key is the id as 8 big-endian bytes, which is how gov stores proposals.
func (cc *ChainConfig) recentProposalsPage(ctx context.Context) (*query.PageRequest, error) {
	resp := &govv1.QueryProposalsResponse{}
	req := &govv1.QueryProposalsRequest{Pagination: &query.PageRequest{Limit: 1, Reverse: true}}
	if err := abciQuery(ctx, cc.client, "/cosmos.gov.v1.Query/Proposals", req, resp); err != nil {
		return nil, err
	}
	if len(resp.Proposals) == 0 {
		return nil, nil
	}
	from := uint64(1)
	if latest := resp.Proposals[0].Id; latest > proposalLookback {
		from = latest - proposalLookback
	}
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, from)
	return &query.PageRequest{Key: key, Limit: proposalLookback + 1}, nil
}

func (cc *ChainConfig) proposalsV1(ctx context.Context) ([]proposalInfo, error) {
	page, err := cc.recentProposalsPage(ctx)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, nil
	}
	resp := &govv1.QueryProposalsResponse{}
	req := &govv1.QueryProposalsRequest{ProposalStatus: govv1.ProposalStatus_PROPOSAL_STATUS_VOTING_PERIOD, Pagination: page}
	if err := abciQuery(ctx, cc.client, "/cosmos.gov.v1.Query/Proposals", req, resp); err != nil {
		return nil, err
	}
	props := make([]proposalInfo, 0, len(resp.Proposals))
	for _, p := range resp.Proposals {
		info := proposalInfo{ID: p.Id, Title: p.Title}
		if p.VotingEndTime != nil {
			info.VotingEnd = *p.VotingEndTime
		}
		for _, m := range p.Messages {
			if isUpgrade(m.TypeUrl) {
				info.IsUpgrade = true
			}
			if m.TypeUrl != "/cosmos.gov.v1.MsgExecLegacyContent" {
				continue
			}
			legacy := &govv1.MsgExecLegacyContent{}
			if legacy.Unmarshal(m.Value) != nil || legacy.Content == nil {
				continue
			}
			if isUpgrade(legacy.Content.TypeUrl) {
				info.IsUpgrade = true
			}
			if info.Title == "" {
				info.Title = legacyTitle(legacy.Content.Value)
			}
		}
		if info.Title == "" {
			info.Title = p.Metadata
		}
		info.Title = shortTitle(info.Title)
		props = append(props, info)
	}
	return props, nil
}

func (cc *ChainConfig) proposalsV1beta1(ctx context.Context) ([]proposalInfo, error) {
	resp := &govv1beta1.QueryProposalsResponse{}
	req := &govv1beta1.QueryProposalsRequest{ProposalStatus: govv1beta1.StatusVotingPeriod}
	if err := abciQuery(ctx, cc.client, "/cosmos.gov.v1beta1.Query/Proposals", req, resp); err != nil {
		return nil, err
	}
	props := make([]proposalInfo, 0, len(resp.Proposals))
	for _, p := range resp.Proposals {
		info := proposalInfo{ID: p.ProposalId, VotingEnd: p.VotingEndTime}
		if p.Content != nil {
			info.IsUpgrade = isUpgrade(p.Content.TypeUrl)
			info.Title = shortTitle(legacyTitle(p.Content.Value))
		}
		props = append(props, info)
	}
	return props, nil
}

// hasVoted asks the chain whether the account voted on a proposal. A "not found" answer means it has not.
func (cc *ChainConfig) hasVoted(ctx context.Context, id uint64, voter string) (bool, error) {
	var err error
	if cc.govLegacy {
		req := &govv1beta1.QueryVoteRequest{ProposalId: id, Voter: voter}
		err = abciQuery(ctx, cc.client, "/cosmos.gov.v1beta1.Query/Vote", req, &govv1beta1.QueryVoteResponse{})
	} else {
		req := &govv1.QueryVoteRequest{ProposalId: id, Voter: voter}
		err = abciQuery(ctx, cc.client, "/cosmos.gov.v1.Query/Vote", req, &govv1.QueryVoteResponse{})
	}
	switch {
	case err == nil:
		return true, nil
	case abciLogContains(err, "not found"):
		return false, nil
	}
	return false, err
}

// refreshGov updates the upgrade plan and the open proposals. Failures are logged when they first appear and
// when they change, not every minute.
func (cc *ChainConfig) refreshGov() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var problems []string
	// nothing is queried that neither an alert, the dashboard nor the exporter will show
	wanted := td.EnableDash || td.Prom
	if wanted || cc.Alerts.UpgradeAlerts {
		if err := cc.refreshUpgrade(ctx); err != nil {
			problems = append(problems, "upgrade plan: "+err.Error())
		}
	}
	// no voter to check for on an rpc-only chain, and consumer chains (identified by consensus address) run
	// without a governance module
	if (wanted || cc.Alerts.GovAlerts) && cc.operator != "" {
		if err := cc.refreshProposals(ctx); err != nil {
			problems = append(problems, "proposals: "+err.Error())
		}
	}
	msg := strings.Join(problems, "; ")
	if msg != cc.govErr && msg != "" {
		l("❓", cc.ChainId, "could not refresh", msg)
	}
	cc.govErr = msg
	if td.Prom {
		remaining := float64(-1)
		if cc.upgrade != nil {
			remaining = float64(cc.upgrade.Height - cc.lastBlockNum)
		}
		unvoted := 0
		for _, p := range cc.proposals {
			if !p.Voted {
				unvoted++
			}
		}
		td.statsChan <- cc.mkUpdate(metricUpgradeBlocksRemaining, remaining, "")
		td.statsChan <- cc.mkUpdate(metricUnvotedProposals, float64(unvoted), "")
		td.statsChan <- cc.mkUpdate(metricVotingPowerTokens, cc.valInfo.Tokens, "")
		td.statsChan <- cc.mkUpdate(metricVotingPowerPercent, cc.valInfo.VotingPowerPct, "")
	}
}

// upgradeEta estimates how long until the scheduled upgrade height. The second value is false when there is not
// enough block timing data yet.
func (cc *ChainConfig) upgradeEta() (time.Duration, bool) {
	if cc.upgrade == nil || cc.avgBlockTime == 0 || cc.lastBlockNum == 0 {
		return 0, false
	}
	remaining := cc.upgrade.Height - cc.lastBlockNum
	if remaining < 0 {
		remaining = 0
	}
	return time.Duration(float64(remaining) * cc.avgBlockTime * float64(time.Second)), true
}

// alertUpgrade raises up to three notices for a scheduled upgrade (scheduled, due soon, imminent) and resolves
// them once the plan is applied or cancelled. The text of each notice is kept so the resolve matches it.
func (cc *ChainConfig) alertUpgrade() {
	if !cc.Alerts.UpgradeAlerts {
		return
	}
	key := ""
	if cc.upgrade != nil {
		key = fmt.Sprintf("%s@%d", cc.upgrade.Name, cc.upgrade.Height)
	}
	// the three notices share one alert id and one open alarm: each new notice takes over from the one before,
	// PagerDuty sees one incident, and a finished upgrade resolves whichever notice is current
	if key != cc.upgradeKey {
		for i := len(cc.upgradeAlarms) - 1; i >= 0; i-- {
			if cc.upgradeAlarms[i] != "" {
				td.alert(cc.name, cc.upgradeAlarms[i], "info", true, cc.alertId("upgrade"))
				break
			}
		}
		alarms.forget(cc.name, cc.upgradeAlarms[:]...)
		cc.upgradeAlarms = [3]string{}
		cc.upgradeKey = key
	}
	if cc.upgrade == nil {
		return
	}
	// the text is the de-duplication key and is saved across restarts, so it must not carry the estimated time:
	// that drifts by a few minutes between refreshes and would turn every restart into a repeat alert.
	// The countdown is on the dashboard.
	p := cc.upgrade
	eta, known := cc.upgradeEta()
	stages := [3]struct {
		due      bool
		msg      string
		severity string
	}{
		{true,
			fmt.Sprintf("upgrade %s is scheduled at height %d on %s", p.Name, p.Height, cc.ChainId),
			"info"},
		{known && eta < time.Duration(cc.Alerts.UpgradeNoticeHours)*time.Hour,
			fmt.Sprintf("upgrade %s on %s is due in under %d hours (height %d)", p.Name, cc.ChainId, cc.Alerts.UpgradeNoticeHours, p.Height),
			"info"},
		{known && eta < time.Duration(cc.Alerts.UpgradeImminentMinutes)*time.Minute,
			fmt.Sprintf("upgrade %s on %s is due in under %d minutes (height %d)", p.Name, cc.ChainId, cc.Alerts.UpgradeImminentMinutes, p.Height),
			cc.Alerts.UpgradePriority},
	}
	// one stage per refresh, so when several are already due at startup they arrive in order a minute apart
	for i, s := range stages {
		if s.due && cc.upgradeAlarms[i] == "" {
			cc.upgradeAlarms[i] = s.msg
			td.alert(cc.name, s.msg, s.severity, false, cc.alertId("upgrade"))
			alarms.forget(cc.name, cc.upgradeAlarms[:i]...)
			return
		}
	}
}

// alertGov reminds about proposals the validator has not voted on, once when they are seen and again when the
// voting period is nearly over, and resolves both once the vote is cast or the period ends.
func (cc *ChainConfig) alertGov() {
	if !cc.Alerts.GovAlerts {
		return
	}
	if cc.govAlarms == nil {
		cc.govAlarms = make(map[uint64]*govAlarm)
	}
	open := make(map[uint64]bool)
	for _, p := range cc.proposals {
		if p.Voted {
			continue
		}
		open[p.ID] = true
		a := cc.govAlarms[p.ID]
		if a == nil {
			a = &govAlarm{}
			cc.govAlarms[p.ID] = a
		}
		kind := "proposal"
		if p.IsUpgrade {
			kind = "upgrade proposal"
		}
		ends := p.VotingEnd.UTC().Format("2006-01-02 15:04 UTC")
		// both notices share one id, so PagerDuty sees one incident per proposal
		id := *cc.alertId(fmt.Sprintf("gov%d", p.ID))
		final := time.Until(p.VotingEnd) < time.Duration(cc.Alerts.GovFinalHours)*time.Hour
		// a proposal first seen inside its final window gets the final notice only, not both at once
		if a.msg == "" && a.finalMsg == "" && !final {
			a.msg = fmt.Sprintf("%s #%d (%s) on %s is open for voting until %s and %s has not voted", kind, p.ID, p.Title, cc.ChainId, ends, cc.valInfo.Moniker)
			td.alert(cc.name, a.msg, cc.Alerts.GovPriority, false, &id)
		}
		if a.finalMsg == "" && final {
			a.finalMsg = fmt.Sprintf("%s #%d (%s) on %s closes in under %d hours (%s) and %s has still not voted", kind, p.ID, p.Title, cc.ChainId, cc.Alerts.GovFinalHours, ends, cc.valInfo.Moniker)
			td.alert(cc.name, a.finalMsg, cc.Alerts.GovPriority, false, &id)
			// the final notice takes over from the open one on the dashboard
			alarms.forget(cc.name, a.msg)
		}
	}
	for pid, a := range cc.govAlarms {
		if open[pid] {
			continue
		}
		// one resolve, for whichever notice is current
		id := *cc.alertId(fmt.Sprintf("gov%d", pid))
		switch {
		case a.finalMsg != "":
			td.alert(cc.name, a.finalMsg, "info", true, &id)
			alarms.forget(cc.name, a.msg)
		case a.msg != "":
			td.alert(cc.name, a.msg, "info", true, &id)
		}
		delete(cc.govAlarms, pid)
	}
}

// alertVotingPower sends a one-off notice when the delegated stake moved by more than the configured share
// between two refreshes. A slow drain over many hours is not caught, only a jump between one minute and the next.
func (cc *ChainConfig) alertVotingPower() {
	if !cc.Alerts.VotingPowerAlerts || cc.lastValInfo == nil {
		return
	}
	prev, cur := cc.lastValInfo.Tokens, cc.valInfo.Tokens
	if prev == 0 || cur == 0 || prev == cur {
		return
	}
	change := 100 * (cur - prev) / prev
	if math.Abs(change) < cc.Alerts.VotingPowerChangePercent {
		return
	}
	// each change is its own PagerDuty incident, otherwise a second change only updates the first one
	id := *cc.alertId("votingpower" + time.Now().UTC().Format("20060102T1504"))
	td.alertEvent(cc.name,
		fmt.Sprintf("voting power of %s on %s changed %+.1f%% (now %.2f%% of bonded stake)",
			cc.valInfo.Moniker, cc.ChainId, change, cc.valInfo.VotingPowerPct),
		cc.Alerts.VotingPowerPriority, &id)
}
