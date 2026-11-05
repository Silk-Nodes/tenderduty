package tenderduty

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	upgrade "cosmossdk.io/x/upgrade/types"
	dash "github.com/Silk-Nodes/tenderduty/v2/td2/dashboard"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v2"
)

const (
	showBLocks = 512
	staleHours = 24
	// configMaxBytes caps a remote configuration download.
	configMaxBytes = 4 << 20
)

// severities are the PagerDuty Events v2 values; anything else is refused at startup because PagerDuty would
// reject the event and the page would be lost silently.
var severities = map[string]bool{"info": true, "warning": true, "error": true, "critical": true}

// Config holds both the settings for tenderduty to monitor and state information while running.
type Config struct {
	alertChan  chan *alertMsg // channel used for outgoing notifications
	updateChan chan *dash.ChainStatus
	logChan    chan dash.LogMessage
	statsChan  chan *promUpdate
	ctx        context.Context
	cancel     context.CancelFunc
	alarms     *alarmCache

	// ConfigVersion, when set, must not go backwards between restarts of a remotely loaded config. Without it any
	// configuration ever encrypted with the password stays valid, so whoever serves the file could serve an old one.
	ConfigVersion int64 `yaml:"config_version"`

	// EnableDash enables the web dashboard
	EnableDash bool `yaml:"enable_dashboard"`
	// Listen is the TCP port for the dashboard
	Listen string `yaml:"listen_port"`
	// ListenAddress is the interface the dashboard and the prometheus exporter bind to. Empty means every interface.
	// Neither has any authentication, so a dashboard that is not meant for the whole network belongs on 127.0.0.1
	// or a private address, with a reverse proxy in front if it has to be reached from further away.
	ListenAddress string `yaml:"listen_address"`
	// HideLogs controls whether logs are sent to the dashboard. It will also suppress many alarm details.
	// This is useful if the dashboard will be public.
	HideLogs bool `yaml:"hide_logs"`

	// NodeDownMin controls how long we wait before sending an alert that a node is not responding or has
	// fallen behind.
	NodeDownMin int `yaml:"node_down_alert_minutes"`
	// NodeDownSeverity controls the Pagerduty severity when notifying if a node is down.
	NodeDownSeverity string `yaml:"node_down_alert_severity"`
	// LagBlocks is how many blocks a node may trail the best node before it is treated as down.
	LagBlocks int `yaml:"lag_blocks"`

	// Prom controls if the prometheus exporter is enabled.
	Prom bool `yaml:"prometheus_enabled"`
	// PrometheusListenPort is the port number used by the prometheus web server
	PrometheusListenPort int `yaml:"prometheus_listen_port"`

	// Pagerduty configuration values
	Pagerduty PDConfig `yaml:"pagerduty"`
	// Discord webhook information
	Discord DiscordConfig `yaml:"discord"`
	// Telegram api information
	Telegram TeleConfig `yaml:"telegram"`
	// Slack webhook information
	Slack SlackConfig `yaml:"slack"`
	// Healthcheck information
	Healthcheck HealthcheckConfig `yaml:"healthcheck"`

	chainsMux sync.RWMutex // prevents concurrent map access for Chains
	// Chains has settings for each validator to monitor. The map's name does not need to match the chain-id.
	Chains map[string]*ChainConfig `yaml:"chains"`
}

// savedState is dumped to a JSON file at exit time, and is loaded at start. If successful it will prevent
// duplicate alerts, and will show old blocks in the dashboard.
type savedState struct {
	Alarms        *alarmCache                     `json:"alarms"`
	Blocks        map[string][]int                `json:"blocks"`
	NodesDown     map[string]map[string]time.Time `json:"nodes_down"`
	ConfigVersion int64                           `json:"config_version,omitempty"`
}

// ChainConfig represents a validator to be monitored on a chain, it is somewhat of a misnomer since multiple
// validators can be monitored on a single chain.
type ChainConfig struct {
	name           string
	clientMux      sync.Mutex // guards client and wsclient while they are being replaced
	wsclient       *websocket.Conn
	client         *rpchttp.HTTP // legit tendermint client
	currentUrl     string        // the node the client is pointed at
	noNodes        bool          // tracks if all nodes are down
	valInfo        *ValInfo      // recent validator state, only refreshed every few minutes
	operator       string        // valoper used for staking and governance queries, found from a valcons when needed
	lastValInfo    *ValInfo      // use for detecting newly-jailed/tombstone
	blocksResults  []int
	lastError      string
	lastBlockTime  time.Time
	lastBlockAlarm bool
	lastBlockNum   int64
	avgBlockTime   float64 // seconds between blocks, smoothed

	upgrade       *upgrade.Plan        // scheduled upgrade from x/upgrade, nil when none
	upgradeKey    string               // name and height of the plan the current notices are about
	upgradeAlarms [3]string            // text of each notice sent for the plan, empty when not sent
	proposals     []proposalInfo       // proposals in their voting period
	govAlarms     map[uint64]*govAlarm // reminders sent per proposal
	govLegacy     bool                 // chain only answers gov v1beta1 queries
	govErr        string               // last refresh problem, so it is logged once
	stalledMsg    string               // text of the open stalled alert, so the resolve matches it

	statTotalSigns      float64
	statTotalProps      float64
	statTotalMiss       float64
	statPrevoteMiss     float64
	statPrecommitMiss   float64
	statConsecutiveMiss float64

	// ChainId is used to ensure any endpoints contacted claim to be on the correct chain. This is a weak verification,
	// no light client validation is performed, so caution is advised when using public endpoints.
	ChainId string `yaml:"chain_id"`
	// ValAddress is the validator operator address to be monitored. Tenderduty v1 required the consensus address,
	// this is no longer needed. The operator address is much easier to find in explorers etc.
	ValAddress string `yaml:"valoper_address"`
	// Alerts defines the types of alerts to send for this chain.
	Alerts AlertConfig `yaml:"alerts"`
	// PublicFallback determines if tenderduty should attempt to use public RPC endpoints in the situation that not
	// explicitly defined RPC servers are available. Not recommended.
	PublicFallback bool `yaml:"public_fallback"`
	// Nodes defines what RPC servers to connect to.
	Nodes []*NodeConfig `yaml:"nodes"`
}

// rpcOnly reports whether the chain is monitored without a validator: node health, stalled detection and
// upgrade alerts still apply, everything about signing, jailing and voting does not.
func (cc *ChainConfig) rpcOnly() bool {
	return cc.ValAddress == ""
}

// alertId is the de-duplication key for an alert: the validator's consensus address plus a suffix, or the
// chain id when the chain is monitored without a validator.
func (cc *ChainConfig) alertId(suffix string) *string {
	// the operator address is known from the config, so the key is the same before and after the validator
	// loads, and the same across instances that page the same PagerDuty service
	id := cc.ChainId + suffix
	if cc.ValAddress != "" {
		id = cc.ValAddress + suffix
	}
	return &id
}

// healthyNodes counts the nodes that passed their last probe.
func (cc *ChainConfig) healthyNodes() int {
	n := 0
	for _, node := range cc.Nodes {
		if !node.down {
			n++
		}
	}
	return n
}

// pushStatus sends the chain's current state to the dashboard.
func (cc *ChainConfig) pushStatus() {
	if !td.EnableDash {
		return
	}
	active := alarms.getCount(cc.name)
	if cc.noNodes && active == 0 {
		active = 1
	}
	nodes := make([]dash.NodeStatus, len(cc.Nodes))
	for i, node := range cc.Nodes {
		nodes[i] = dash.NodeStatus{
			Name:    node.Url,
			Up:      !node.down,
			Active:  node.Url == cc.currentUrl,
			Height:  node.height,
			Lag:     node.lag,
			Syncing: node.syncing,
			Message: node.lastMsg,
		}
	}
	var up *dash.UpgradeStatus
	if cc.upgrade != nil {
		up = &dash.UpgradeStatus{Name: cc.upgrade.Name, Height: cc.upgrade.Height, EtaSeconds: -1}
		if eta, ok := cc.upgradeEta(); ok {
			up.EtaSeconds = int64(eta.Seconds())
		}
	}
	props := make([]dash.ProposalStatus, len(cc.proposals))
	for i, p := range cc.proposals {
		props[i] = dash.ProposalStatus{ID: p.ID, Title: p.Title, Ends: p.VotingEnd.Unix(), IsUpgrade: p.IsUpgrade, Voted: p.Voted}
	}
	var lastBlock int64
	if !cc.lastBlockTime.IsZero() {
		lastBlock = cc.lastBlockTime.Unix()
	}
	td.updateChan <- &dash.ChainStatus{
		MsgType:        "status",
		Name:           cc.name,
		ChainId:        cc.ChainId,
		Validator:      !cc.rpcOnly(),
		Moniker:        cc.valInfo.Moniker,
		Bonded:         cc.valInfo.Bonded,
		Jailed:         cc.valInfo.Jailed,
		Tombstoned:     cc.valInfo.Tombstoned,
		Missed:         cc.valInfo.Missed,
		Window:         cc.valInfo.Window,
		JailAfter:      cc.valInfo.JailAfter,
		Nodes:          len(cc.Nodes),
		HealthyNodes:   cc.healthyNodes(),
		ActiveAlerts:   active,
		Height:         cc.lastBlockNum,
		LastError:      cc.lastError,
		LastBlock:      lastBlock,
		AvgBlockTime:   cc.avgBlockTime,
		StalledMinutes: cc.Alerts.Stalled,
		Tokens:         cc.valInfo.Tokens,
		VotingPowerPct: cc.valInfo.VotingPowerPct,
		Upgrade:        up,
		Proposals:      props,
		NodeStatus:     nodes,
		Alerts:         listAlarms(cc.name),
		Blocks:         cc.blocksResults,
	}
}

// mkUpdate returns the info needed by prometheus for a gauge.
func (cc *ChainConfig) mkUpdate(t metricType, v float64, node string) *promUpdate {
	return &promUpdate{
		metric:   t,
		counter:  v,
		name:     cc.name,
		chainId:  cc.ChainId,
		moniker:  cc.valInfo.Moniker,
		endpoint: node,
	}
}

// AlertConfig defines the type of alerts to send for a ChainConfig
type AlertConfig struct {
	// How many minutes to wait before alerting that no new blocks have been seen
	Stalled int `yaml:"stalled_minutes"`
	// Whether to alert when no new blocks are seen
	StalledAlerts bool `yaml:"stalled_enabled"`

	// How many missed blocks are acceptable before alerting
	ConsecutiveMissed int `yaml:"consecutive_missed"`
	// Tag for pagerduty to set the alert priority
	ConsecutivePriority string `yaml:"consecutive_priority"`
	// StalledPriority is the severity of the stalled chain alert, critical unless set. Lower it to info on a chain
	// whose nodes go down for planned maintenance, so the notice reaches chat without paging.
	StalledPriority string `yaml:"stalled_priority"`
	// Whether to alert on consecutive missed blocks
	ConsecutiveAlerts bool `yaml:"consecutive_enabled"`

	// Window is how many blocks missed as a percentage of the slashing window to trigger an alert
	Window int `yaml:"percentage_missed"`
	// PercentagePriority is a tag for pagerduty to route on priority
	PercentagePriority string `yaml:"percentage_priority"`
	// PercentageAlerts is whether to alert on percentage based misses
	PercentageAlerts bool `yaml:"percentage_enabled"`

	// AlertIfInactive decides if tenderduty send an alert if the validator is not in the active set?
	AlertIfInactive bool `yaml:"alert_if_inactive"`
	// AlertIfNoServers: should an alert be sent if no servers are reachable?
	AlertIfNoServers bool `yaml:"alert_if_no_servers"`

	// UpgradeAlerts: notify when x/upgrade has a scheduled plan, with reminders as the height approaches
	UpgradeAlerts bool `yaml:"upgrade_enabled"`
	// UpgradeNoticeHours: send a warning when the upgrade is estimated to be under this many hours away
	UpgradeNoticeHours int `yaml:"upgrade_notice_hours"`
	// UpgradeImminentMinutes: send the final notice when the upgrade is under this many minutes away
	UpgradeImminentMinutes int `yaml:"upgrade_imminent_minutes"`
	// UpgradePriority is the pagerduty severity of the final notice
	UpgradePriority string `yaml:"upgrade_priority"`

	// GovAlerts: remind when a proposal is in its voting period and the validator has not voted
	GovAlerts bool `yaml:"governance_enabled"`
	// GovFinalHours: send a second reminder when voting closes in under this many hours
	GovFinalHours int `yaml:"governance_final_hours"`
	// GovPriority is the pagerduty severity of governance reminders
	GovPriority string `yaml:"governance_priority"`

	// VotingPowerAlerts: notify when the delegated stake jumps or drops between two refreshes
	VotingPowerAlerts bool `yaml:"voting_power_enabled"`
	// VotingPowerChangePercent is the size of change, as a percentage of the previous stake, that triggers a notice
	VotingPowerChangePercent float64 `yaml:"voting_power_change_percent"`
	// VotingPowerPriority is the pagerduty severity of voting power notices
	VotingPowerPriority string `yaml:"voting_power_priority"`

	// PagerdutyAlerts: Should pagerduty alerts be sent for this chain? Both 'config.pagerduty.enabled: yes' and this must be set.
	//Deprecated: use Pagerduty.Enabled instead
	PagerdutyAlerts bool `yaml:"pagerduty_alerts"`
	// DiscordAlerts: Should discord alerts be sent for this chain? Both 'config.discord.enabled: yes' and this must be set.
	//Deprecated: use Discord.Enabled instead
	DiscordAlerts bool `yaml:"discord_alerts"`
	// TelegramAlerts: Should telegram alerts be sent for this chain? Both 'config.telegram.enabled: yes' and this must be set.
	//Deprecated: use Telegram.Enabled instead
	TelegramAlerts bool `yaml:"telegram_alerts"`

	// chain specific overrides for alert destinations.
	// Pagerduty configuration values
	Pagerduty PDConfig `yaml:"pagerduty"`
	// Discord webhook information
	Discord DiscordConfig `yaml:"discord"`
	// Telegram webhook information
	Telegram TeleConfig `yaml:"telegram"`
	// Slack webhook information
	Slack SlackConfig `yaml:"slack"`
}

// NodeConfig holds the basic information for a node to connect to.
type NodeConfig struct {
	Url         string `yaml:"url"`
	AlertIfDown bool   `yaml:"alert_if_down"`

	down       bool
	wasDown    bool
	syncing    bool
	lastMsg    string
	downSince  time.Time
	height     int64     // last height the node reported
	lag        int64     // blocks behind the best node at the last probe
	wsFailedAt time.Time // when the websocket to this node last stopped delivering blocks
}

// PDConfig is the information required to send alerts to PagerDuty
type PDConfig struct {
	Enabled         bool   `yaml:"enabled"`
	ApiKey          string `yaml:"api_key"`
	DefaultSeverity string `yaml:"default_severity"`
	// SeverityThreshold is the lowest severity sent to this channel: info, warning, error or critical.
	SeverityThreshold string `yaml:"severity_threshold"`
}

// DiscordConfig holds the information needed to publish to a Discord webhook for sending alerts
type DiscordConfig struct {
	Enabled           bool     `yaml:"enabled"`
	Webhook           string   `yaml:"webhook"`
	Mentions          []string `yaml:"mentions"`
	SeverityThreshold string   `yaml:"severity_threshold"`
}

// TeleConfig holds the information needed to publish to a Telegram webhook for sending alerts
type TeleConfig struct {
	Enabled           bool     `yaml:"enabled"`
	ApiKey            string   `yaml:"api_key"`
	Channel           string   `yaml:"channel"`
	Mentions          []string `yaml:"mentions"`
	SeverityThreshold string   `yaml:"severity_threshold"`
}

// SlackConfig holds the information needed to publish to a Slack webhook for sending alerts
type SlackConfig struct {
	Enabled           bool     `yaml:"enabled"`
	Webhook           string   `yaml:"webhook"`
	Mentions          []string `yaml:"mentions"`
	SeverityThreshold string   `yaml:"severity_threshold"`
}

// HealthcheckConfig holds the information needed to send pings to a healthcheck endpoint
type HealthcheckConfig struct {
	Enabled  bool          `yaml:"enabled"`
	PingURL  string        `yaml:"ping_url"`
	PingRate time.Duration `yaml:"ping_rate"`
}

// validateConfig is a non-exhaustive check for common problems with the configuration. Needs love.
func validateConfig(c *Config) (fatal bool, problems []string) {
	problems = make([]string, 0)
	var err error

	if c.EnableDash {
		_, err = url.Parse(c.Listen)
		if err != nil {
			fatal = true
			problems = append(problems, fmt.Sprintf("error: The listen URL %s does not appear to be valid", c.Listen))
		}
	}
	if c.ListenAddress != "" && net.ParseIP(c.ListenAddress) == nil {
		fatal = true
		problems = append(problems, fmt.Sprintf("error: listen_address %s is not an IP address", c.ListenAddress))
	}

	if c.Pagerduty.Enabled {
		rex := regexp.MustCompile(`[+_-]`)
		if rex.MatchString(c.Pagerduty.ApiKey) {
			fatal = true
			problems = append(problems, "error: The Pagerduty key provided appears to be an Oauth token, not a V2 Events API key.")
		}
	}

	if c.NodeDownSeverity != "" && !severities[c.NodeDownSeverity] {
		fatal = true
		problems = append(problems, fmt.Sprintf("error: node_down_alert_severity %q is not one of info, warning, error, critical", c.NodeDownSeverity))
	}
	if c.NodeDownSeverity == "" {
		c.NodeDownSeverity = "critical"
	}
	if c.NodeDownMin < 3 {
		problems = append(problems, "warning: setting 'node_down_alert_minutes' to less than three minutes might result in false alarms")
	}
	if c.LagBlocks <= 0 {
		c.LagBlocks = 10
	}
	// pagerduty is for things that need a person, the chat channels get everything
	if c.Pagerduty.SeverityThreshold == "" {
		c.Pagerduty.SeverityThreshold = "warning"
	}
	for _, t := range []*string{&c.Discord.SeverityThreshold, &c.Telegram.SeverityThreshold, &c.Slack.SeverityThreshold} {
		if *t == "" {
			*t = "info"
		}
	}
	if c.Healthcheck.Enabled && c.Healthcheck.PingRate <= 0 {
		c.Healthcheck.PingRate = 60
	}

	var wantsPublic bool
	for k, v := range c.Chains {
		if v.blocksResults == nil {
			v.blocksResults = make([]int, showBLocks)
			for i := range v.blocksResults {
				v.blocksResults[i] = -1
			}
		}
		if v.name == "" {
			v.name = k
		}
		if v.PublicFallback {
			wantsPublic = true
		}

		v.valInfo = &ValInfo{Moniker: "not connected"}
		if v.rpcOnly() {
			v.valInfo.Moniker = ""
		}

		// defaults for the newer alert types, so a config that only sets the enabled flag works
		setDefault := func(n *int, d int) {
			if *n <= 0 {
				*n = d
			}
		}
		setDefault(&v.Alerts.UpgradeNoticeHours, 24)
		setDefault(&v.Alerts.UpgradeImminentMinutes, 60)
		setDefault(&v.Alerts.GovFinalHours, 24)
		if v.Alerts.StalledPriority == "" {
			v.Alerts.StalledPriority = "critical"
		}
		if v.Alerts.VotingPowerChangePercent <= 0 {
			v.Alerts.VotingPowerChangePercent = 10
		}
		// these are notices rather than incidents, so they stay below the pagerduty threshold unless raised
		if v.Alerts.UpgradePriority == "" {
			v.Alerts.UpgradePriority = "info"
		}
		if v.Alerts.GovPriority == "" {
			v.Alerts.GovPriority = "info"
		}
		if v.Alerts.VotingPowerPriority == "" {
			v.Alerts.VotingPowerPriority = "info"
		}
		for name, sev := range map[string]string{
			"consecutive_priority": v.Alerts.ConsecutivePriority, "percentage_priority": v.Alerts.PercentagePriority,
			"stalled_priority": v.Alerts.StalledPriority, "upgrade_priority": v.Alerts.UpgradePriority,
			"governance_priority": v.Alerts.GovPriority, "voting_power_priority": v.Alerts.VotingPowerPriority,
		} {
			if sev != "" && !severities[sev] {
				fatal = true
				problems = append(problems, fmt.Sprintf("error: %s %s %q is not one of info, warning, error, critical", k, name, sev))
			}
		}

		// the bools for enabling alerts are deprecated with full configs preferred,
		// don't break if someone is still using them:
		if v.Alerts.DiscordAlerts && !v.Alerts.Discord.Enabled {
			v.Alerts.Discord.Enabled = true
		}
		if v.Alerts.TelegramAlerts && !v.Alerts.Telegram.Enabled {
			v.Alerts.Telegram.Enabled = true
		}
		if v.Alerts.PagerdutyAlerts && !v.Alerts.Pagerduty.Enabled {
			v.Alerts.Pagerduty.Enabled = true
		}

		// if the settings are blank, copy in the defaults:
		if v.Alerts.Discord.Webhook == "" {
			v.Alerts.Discord.Webhook = c.Discord.Webhook
			v.Alerts.Discord.Mentions = c.Discord.Mentions
		}
		if v.Alerts.Slack.Webhook == "" {
			v.Alerts.Slack.Webhook = c.Slack.Webhook
			v.Alerts.Slack.Mentions = c.Slack.Mentions
		}
		if v.Alerts.Telegram.ApiKey == "" {
			v.Alerts.Telegram.ApiKey = c.Telegram.ApiKey
			v.Alerts.Telegram.Mentions = c.Telegram.Mentions
		}
		if v.Alerts.Telegram.Channel == "" {
			v.Alerts.Telegram.Channel = c.Telegram.Channel
		}
		if v.Alerts.Pagerduty.ApiKey == "" {
			v.Alerts.Pagerduty.ApiKey = c.Pagerduty.ApiKey
			v.Alerts.Pagerduty.DefaultSeverity = c.Pagerduty.DefaultSeverity
		}
		if v.Alerts.Pagerduty.SeverityThreshold == "" {
			v.Alerts.Pagerduty.SeverityThreshold = c.Pagerduty.SeverityThreshold
		}
		if v.Alerts.Discord.SeverityThreshold == "" {
			v.Alerts.Discord.SeverityThreshold = c.Discord.SeverityThreshold
		}
		if v.Alerts.Telegram.SeverityThreshold == "" {
			v.Alerts.Telegram.SeverityThreshold = c.Telegram.SeverityThreshold
		}
		if v.Alerts.Slack.SeverityThreshold == "" {
			v.Alerts.Slack.SeverityThreshold = c.Slack.SeverityThreshold
		}

		switch {
		case v.Alerts.Slack.Enabled && !c.Slack.Enabled:
			problems = append(problems, fmt.Sprintf("warn: %20s is configured for slack alerts, but it is not enabled", k))
			fallthrough
		case v.Alerts.Discord.Enabled && !c.Discord.Enabled:
			problems = append(problems, fmt.Sprintf("warn: %20s is configured for discord alerts, but it is not enabled", k))
			fallthrough
		case v.Alerts.Pagerduty.Enabled && !c.Pagerduty.Enabled:
			problems = append(problems, fmt.Sprintf("warn: %20s is configured for pagerduty alerts, but it is not enabled", k))
			fallthrough
		case v.Alerts.Telegram.Enabled && !c.Telegram.Enabled:
			problems = append(problems, fmt.Sprintf("warn: %20s is configured for telegram alerts, but it is not enabled", k))
		case !v.Alerts.ConsecutiveAlerts && !v.Alerts.PercentageAlerts && !v.Alerts.AlertIfInactive && !v.Alerts.AlertIfNoServers &&
			!v.Alerts.StalledAlerts && !v.Alerts.UpgradeAlerts && !v.Alerts.GovAlerts && !v.Alerts.VotingPowerAlerts:
			problems = append(problems, fmt.Sprintf("warn: %20s has no alert types configured", k))
			fallthrough
		case !v.Alerts.Pagerduty.Enabled && !v.Alerts.Discord.Enabled && !v.Alerts.Telegram.Enabled && !v.Alerts.Slack.Enabled:
			problems = append(problems, fmt.Sprintf("warn: %20s has no notifications configured", k))
		}
		v.pushStatus()
	}

	// if public endpoints are enabled we do our best to keep the list refreshed. Immediate, then every 12 hours.
	if wantsPublic {
		go func() {
			e := refreshRegistry()
			if e != nil {
				l("could not fetch chain registry paths, public fallback unavailable until the next refresh -", e)
			}
			for {
				time.Sleep(12 * time.Hour)
				l("refreshing cosmos.registry paths")
				e = refreshRegistry()
				if e != nil {
					l("could not refresh registry paths -", e)
				}
			}
		}()
	}
	return
}

func loadChainConfig(yamlFile string) (*ChainConfig, error) {
	//#nosec -- variable specified on command line
	f, e := os.OpenFile(yamlFile, os.O_RDONLY, 0600)
	if e != nil {
		return nil, e
	}
	i, e := f.Stat()
	if e != nil {
		_ = f.Close()
		return nil, e
	}
	b := make([]byte, int(i.Size()))
	_, e = f.Read(b)
	_ = f.Close()
	if e != nil {
		return nil, e
	}
	c := &ChainConfig{}
	e = yaml.Unmarshal(b, c)
	if e != nil {
		return nil, e
	}
	return c, nil
}

// loadConfig creates a new Config from a file.
func loadConfig(yamlFile, stateFile, chainConfigDirectory string, password *string) (*Config, error) {

	c := &Config{}
	if strings.HasPrefix(yamlFile, "http://") || strings.HasPrefix(yamlFile, "https://") {
		if *password == "" {
			return nil, errors.New("a password is required if loading a remote configuration")
		}
		if !strings.HasPrefix(yamlFile, "https://") {
			return nil, errors.New("a remote configuration must be loaded over https")
		}
		client := &http.Client{Timeout: 30 * time.Second}
		//#nosec -- url is specified on command line
		resp, err := client.Get(yamlFile)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, configMaxBytes))
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("remote configuration returned %d", resp.StatusCode)
		}
		if u, e := url.Parse(yamlFile); e == nil {
			log.Printf("downloaded %d bytes from %s", len(b), u.Redacted())
		}
		decrypted, err := decrypt(b, *password)
		if err != nil {
			return nil, err
		}
		*password = ""              // the caller's copy, not a local one; the string in main() is what lingered
		_ = os.Unsetenv("PASSWORD") // and the environment variable
		err = yaml.Unmarshal(decrypted, c)
		if err != nil {
			return nil, err
		}
		// authentic is not the same as current: refuse a config older than one already accepted
		if c.ConfigVersion > 0 {
			prev := &savedState{}
			if sb, e := os.ReadFile(stateFile); e == nil { // #nosec G304 -- the state path is a command line flag
				_ = json.Unmarshal(sb, prev)
			}
			if c.ConfigVersion < prev.ConfigVersion {
				return nil, fmt.Errorf("remote configuration version %d is older than the accepted version %d", c.ConfigVersion, prev.ConfigVersion)
			}
		}
	} else {
		//#nosec -- variable specified on command line
		f, e := os.OpenFile(yamlFile, os.O_RDONLY, 0600)
		if e != nil {
			return nil, e
		}
		i, e := f.Stat()
		if e != nil {
			_ = f.Close()
			return nil, e
		}
		b := make([]byte, int(i.Size()))
		_, e = f.Read(b)
		_ = f.Close()
		if e != nil {
			return nil, e
		}
		e = yaml.Unmarshal(b, c)
		if e != nil {
			return nil, e
		}
	}

	// Load additional chain configuration files
	chainConfigFiles, e := os.ReadDir(chainConfigDirectory)
	if e != nil {
		l("Failed to scan chainConfigDirectory", e)
	}

	for _, chainConfigFile := range chainConfigFiles {
		if chainConfigFile.IsDir() {
			l("Skipping Directory: ", chainConfigFile.Name())
			continue
		}
		if !strings.HasSuffix(chainConfigFile.Name(), ".yml") {
			l("Skipping non .yml file: ", chainConfigFile.Name())
			continue
		}
		fmt.Println("Reading Chain Config File: ", chainConfigFile.Name())
		chainConfig, e := loadChainConfig(path.Join(chainConfigDirectory, chainConfigFile.Name()))
		if e != nil {
			l(fmt.Sprintf("Failed to read %s", chainConfigFile), e)
			return nil, e
		}

		chainName := strings.Split(chainConfigFile.Name(), ".")[0]

		// Create map if it didnt exist in config.yml
		if c.Chains == nil {
			c.Chains = make(map[string]*ChainConfig)
		}
		c.Chains[chainName] = chainConfig
		l(fmt.Sprintf("Added %s from ", chainName), chainConfigFile.Name())
	}

	if len(c.Chains) == 0 {
		return nil, errors.New("no chains configured")
	}

	c.alertChan = make(chan *alertMsg)
	c.logChan = make(chan dash.LogMessage)
	// buffer enough to get through validateConfig()
	c.updateChan = make(chan *dash.ChainStatus, len(c.Chains)*2)
	c.statsChan = make(chan *promUpdate, len(c.Chains)*2)
	c.ctx, c.cancel = context.WithCancel(context.Background()) // #nosec G118 -- cancel is kept on the config and called when Run returns

	// handle cached data. FIXME: incomplete.
	c.alarms = &alarmCache{
		SentPdAlarms:  make(map[string]time.Time),
		SentTgAlarms:  make(map[string]time.Time),
		SentDiAlarms:  make(map[string]time.Time),
		SentSlkAlarms: make(map[string]time.Time),
		AllAlarms:     make(map[string]map[string]time.Time),
		notifyMux:     sync.RWMutex{},
	}

	saved := &savedState{}
	//#nosec -- variable specified on command line
	if b, e := os.ReadFile(stateFile); e != nil {
		l("no saved state to restore:", e.Error())
	} else if e = json.Unmarshal(b, saved); e != nil {
		l("could not unmarshal saved state", e.Error())
	}
	for k, v := range saved.Blocks {
		if c.Chains[k] != nil {
			c.Chains[k].blocksResults = v
		}
	}

	// restore alarm state to prevent duplicate alerts
	if saved.Alarms != nil {
		if saved.Alarms.SentTgAlarms != nil {
			alarms.SentTgAlarms = saved.Alarms.SentTgAlarms
			clearStale(alarms.SentTgAlarms, "telegram", c.Pagerduty.Enabled, staleHours)
		}
		if saved.Alarms.SentPdAlarms != nil {
			alarms.SentPdAlarms = saved.Alarms.SentPdAlarms
			clearStale(alarms.SentPdAlarms, "PagerDuty", c.Pagerduty.Enabled, staleHours)
		}
		if saved.Alarms.SentDiAlarms != nil {
			alarms.SentDiAlarms = saved.Alarms.SentDiAlarms
			clearStale(alarms.SentDiAlarms, "Discord", c.Pagerduty.Enabled, staleHours)
		}
		if saved.Alarms.SentSlkAlarms != nil {
			alarms.SentSlkAlarms = saved.Alarms.SentSlkAlarms
			clearStale(alarms.SentSlkAlarms, "Slack", c.Pagerduty.Enabled, staleHours)
		}
		if saved.Alarms.AllAlarms != nil {
			alarms.AllAlarms = saved.Alarms.AllAlarms
			for name, alrm := range saved.Alarms.AllAlarms {
				clearStale(alrm, "dashboard", c.Pagerduty.Enabled, staleHours)
				// an alarm the chain can no longer produce would never resolve: the chain left the config, or it
				// is RPC-only now and the alarm is about its former validator
				cc := c.Chains[name]
				for msg := range alrm {
					_, kind := alertKind(msg)
					validatorOnly := kind == "Governance" || kind == "Voting power" || kind == "Missed blocks" || kind == "Validator inactive"
					if cc == nil || (cc.rpcOnly() && validatorOnly) {
						l(fmt.Sprintf("🗑 dropping restored alarm that %s can no longer resolve - %s", name, msg))
						delete(alrm, msg)
					}
				}
			}
		}
	}

	// we need to know if the node was already down to clear alarms
	if saved.NodesDown != nil {
		for k, v := range saved.NodesDown {
			for nodeUrl := range v {
				if !v[nodeUrl].IsZero() {
					if c.Chains[k] != nil {
						for j := range c.Chains[k].Nodes {
							if c.Chains[k].Nodes[j].Url == nodeUrl {
								c.Chains[k].Nodes[j].down = true
								c.Chains[k].Nodes[j].downSince = v[nodeUrl]
							}
						}
					}
				}
			}
		}
		// now we need to know if all RPC endpoints were down.
		for k, v := range c.Chains {
			downCount := 0
			for j := range v.Nodes {
				if v.Nodes[j].down {
					downCount += 1
				}
			}
			if downCount == len(c.Chains[k].Nodes) {
				c.Chains[k].noNodes = true
			}
		}
	}

	return c, nil
}

func clearStale(alarms map[string]time.Time, what string, hasPagerduty bool, hours float64) {
	for k := range alarms {
		if time.Since(alarms[k]).Hours() >= hours {
			l(fmt.Sprintf("🗑 not restoring old alarm (%v >%.2f hours) from cache - %s", alarms[k], hours, k))
			if hasPagerduty && what == "PagerDuty" {
				l("NOTE: stale alarms may need to be manually cleared from PagerDuty!")
			}
			delete(alarms, k)
			continue
		}
		l(fmt.Sprintf("📂 restored %s alarm state - %s", what, k))
	}
}

// listenAddr is where the dashboard listens; an empty listen_address means every interface.
func (c *Config) listenAddr() string {
	return net.JoinHostPort(c.ListenAddress, c.Listen)
}
