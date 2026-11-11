package dash

type ChainStatus struct {
	MsgType      string `json:"msgType"`
	Name         string `json:"name"`
	ChainId      string `json:"chain_id"`
	Validator    bool   `json:"validator"` // false when the chain is monitored for its nodes and upgrades only
	Moniker      string `json:"moniker"`
	Bonded       bool   `json:"bonded"`
	Jailed       bool   `json:"jailed"`
	Tombstoned   bool   `json:"tombstoned"`
	Missed       int64  `json:"missed"`
	Window       int64  `json:"window"`
	JailAfter    int64  `json:"jail_after"` // missed blocks in the window beyond which the validator is jailed
	Nodes        int    `json:"nodes"`
	HealthyNodes int    `json:"healthy_nodes"`
	ActiveAlerts int    `json:"active_alerts"`
	Height       int64  `json:"height"`
	LastError    string `json:"last_error"`

	LastBlock      int64            `json:"last_block"`     // unix seconds of the last block seen, 0 before the first
	AvgBlockTime   float64          `json:"avg_block_time"` // seconds
	StalledMinutes int              `json:"stalled_minutes"`
	Tokens         float64          `json:"tokens"`
	VotingPowerPct float64          `json:"voting_power_pct"`
	Upgrade        *UpgradeStatus   `json:"upgrade"`
	Proposals      []ProposalStatus `json:"proposals"`
	NodeStatus     []NodeStatus     `json:"node_status"`
	Alerts         []AlertStatus    `json:"alerts"`

	Blocks []int `json:"blocks"`
}

// NodeStatus is one RPC node as seen at the last probe.
type NodeStatus struct {
	Name    string `json:"name"` // the url as configured
	Up      bool   `json:"up"`
	Active  bool   `json:"active"` // the node the websocket is connected to
	Height  int64  `json:"height"`
	Lag     int64  `json:"lag"`
	Syncing bool   `json:"syncing"`
	Message string `json:"message"`
}

// UpgradeStatus is the scheduled x/upgrade plan.
type UpgradeStatus struct {
	Name       string `json:"name"`
	Height     int64  `json:"height"`
	EtaSeconds int64  `json:"eta_seconds"` // -1 when there is not enough block timing data
}

// ProposalStatus is a proposal in its voting period.
type ProposalStatus struct {
	ID        uint64 `json:"id"`
	Title     string `json:"title"`
	Ends      int64  `json:"ends"` // unix seconds
	IsUpgrade bool   `json:"is_upgrade"`
	// the plan an upgrade proposal would schedule, so the dashboard can show an upgrade before the vote closes
	UpgradeName   string `json:"upgrade_name,omitempty"`
	UpgradeHeight int64  `json:"upgrade_height,omitempty"`
	Voted         bool   `json:"voted"`
}

// AlertStatus is an active alarm.
type AlertStatus struct {
	Message  string `json:"message"`
	Since    int64  `json:"since"` // unix seconds
	Severity string `json:"severity,omitempty"`
}

type LogMessage struct {
	MsgType string `json:"msgType"`
	Ts      int64  `json:"ts"`
	Msg     string `json:"msg"`
}
