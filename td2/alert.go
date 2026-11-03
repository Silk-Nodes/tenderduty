package tenderduty

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PagerDuty/go-pagerduty"
	dash "github.com/Silk-Nodes/tenderduty/v2/td2/dashboard"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type alertMsg struct {
	pd   bool
	disc bool
	tg   bool
	slk  bool

	severity string
	resolved bool
	chain    string
	chainId  string
	message  string
	uniqueId string
	key      string

	tgChannel  string
	tgKey      string
	tgMentions string

	discHook     string
	discMentions string

	slkHook     string
	slkMentions string
}

type notifyDest uint8

const (
	pd notifyDest = iota
	tg
	di
	slk
)

type alarmCache struct {
	SentPdAlarms   map[string]time.Time            `json:"sent_pd_alarms"`
	SentTgAlarms   map[string]time.Time            `json:"sent_tg_alarms"`
	SentDiAlarms   map[string]time.Time            `json:"sent_di_alarms"`
	SentSlkAlarms  map[string]time.Time            `json:"sent_slk_alarms"`
	AllAlarms      map[string]map[string]time.Time `json:"sent_all_alarms"`
	flappingAlarms map[string]map[string]time.Time
	notifyMux      sync.RWMutex
}

func (a *alarmCache) getCount(chain string) int {
	if a.AllAlarms == nil || a.AllAlarms[chain] == nil {
		return 0
	}
	a.notifyMux.RLock()
	defer a.notifyMux.RUnlock()
	return len(a.AllAlarms[chain])
}

// alarms is used to prevent double notifications. TODO: save on exit / load on start
var alarms = &alarmCache{
	SentPdAlarms:   make(map[string]time.Time),
	SentTgAlarms:   make(map[string]time.Time),
	SentDiAlarms:   make(map[string]time.Time),
	SentSlkAlarms:  make(map[string]time.Time),
	AllAlarms:      make(map[string]map[string]time.Time),
	flappingAlarms: make(map[string]map[string]time.Time),
	notifyMux:      sync.RWMutex{},
}

func shouldNotify(msg *alertMsg, dest notifyDest) bool {
	alarms.notifyMux.Lock()
	defer alarms.notifyMux.Unlock()
	var whichMap map[string]time.Time
	var service string
	if alarms.AllAlarms[msg.chain] == nil {
		alarms.AllAlarms[msg.chain] = make(map[string]time.Time)
	}
	switch dest {
	case pd:
		whichMap = alarms.SentPdAlarms
		service = "PagerDuty"
	case tg:
		whichMap = alarms.SentTgAlarms
		service = "Telegram"
	case di:
		whichMap = alarms.SentDiAlarms
		service = "Discord"
	case slk:
		whichMap = alarms.SentSlkAlarms
		service = "Slack"
	}

	switch {
	case !whichMap[msg.message].IsZero() && !msg.resolved:
		// already sent this alert
		return false
	case !whichMap[msg.message].IsZero() && msg.resolved:
		// alarm is cleared
		delete(whichMap, msg.message)
		l(fmt.Sprintf("💜 Resolved     alarm on %s (%s) - notifying %s", msg.chain, msg.message, service))
		return true
	case msg.resolved:
		// it looks like we got a duplicate resolution or suppressed it. Note it and move on:
		l(fmt.Sprintf("😕 Not clearing alarm on %s (%s) - no corresponding alert %s", msg.chain, msg.message, service))
		return false
	}

	// check if the alarm is flapping, if we sent the same alert in the last five minutes, show a warning but don't alert
	if alarms.flappingAlarms[msg.chain] == nil {
		alarms.flappingAlarms[msg.chain] = make(map[string]time.Time)
	}

	// for pagerduty we perform some basic flap detection
	if dest == pd && msg.pd && alarms.flappingAlarms[msg.chain][msg.message].After(time.Now().Add(-5*time.Minute)) {
		l("🛑 flapping detected - suppressing pagerduty notification:", msg.chain, msg.message)
		return false
	} else if dest == pd && msg.pd {
		alarms.flappingAlarms[msg.chain][msg.message] = time.Now()
	}

	l(fmt.Sprintf("🚨 ALERT        new alarm on %s (%s) - notifying %s", msg.chain, msg.message, service))
	whichMap[msg.message] = time.Now()
	return true
}

func notifySlack(msg *alertMsg) (err error) {
	if !msg.slk {
		return
	}
	if !shouldNotify(msg, slk) {
		return
	}
	data, err := json.Marshal(buildSlackMessage(msg))
	if err != nil {
		return
	}

	req, err := http.NewRequest("POST", msg.slkHook, bytes.NewBuffer(data))
	if err != nil {
		return
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("could not notify slack for %s got %d response", msg.chain, resp.StatusCode)
	}

	return
}

type SlackMessage struct {
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments"`
}

type Attachment struct {
	Text      string `json:"text"`
	Color     string `json:"color"`
	Title     string `json:"title"`
	TitleLink string `json:"title_link"`
}

func buildSlackMessage(msg *alertMsg) *SlackMessage {
	_, color := severityColor(msg)
	return &SlackMessage{
		Text: strings.TrimSpace(msg.slkMentions),
		Attachments: []Attachment{
			{
				Title: alertHeading(msg),
				Text:  alertBody(msg) + "\n" + msg.chainId,
				Color: color,
			},
		},
	}
}

func notifyDiscord(msg *alertMsg) (err error) {
	if !msg.disc {
		return nil
	}
	if !shouldNotify(msg, di) {
		return nil
	}
	discPost := buildDiscordMessage(msg)
	client := &http.Client{}
	data, err := json.MarshalIndent(discPost, "", "  ")
	if err != nil {
		l("⚠️ Could not notify discord!", err)
		return err
	}

	// a webhook takes five posts per two seconds; a burst of alerts (several proposals at once) runs into that,
	// so a 429 waits for the window Discord names and tries again rather than losing the message
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequest("POST", msg.discHook, bytes.NewBuffer(data))
		if err != nil {
			l("⚠️ Could not notify discord!", err)
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			l("⚠️ Could not notify discord!", err)
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == 204:
			return nil
		case resp.StatusCode == 429:
			wait := 2 * time.Second
			if s, e := strconv.ParseFloat(resp.Header.Get("X-RateLimit-Reset-After"), 64); e == nil && s > 0 && s < 30 {
				wait = time.Duration(s*float64(time.Second)) + 250*time.Millisecond
			}
			l(fmt.Sprintf("⏳ discord rate limit, retrying in %.1fs", wait.Seconds()))
			time.Sleep(wait)
		default:
			l(fmt.Sprintf("⚠️ Could not notify discord! Returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
			return fmt.Errorf("discord returned %d", resp.StatusCode)
		}
	}
	l("⚠️ Could not notify discord! Rate limited on every attempt")
	return errors.New("discord rate limited")
}

type DiscordMessage struct {
	Username  string         `json:"username,omitempty"`
	AvatarUrl string         `json:"avatar_url,omitempty"`
	Content   string         `json:"content"`
	Embeds    []DiscordEmbed `json:"embeds,omitempty"`
}

type DiscordEmbed struct {
	Title       string         `json:"title,omitempty"`
	Url         string         `json:"url,omitempty"`
	Description string         `json:"description"`
	Color       uint           `json:"color"`
	Fields      []DiscordField `json:"fields,omitempty"`
	Timestamp   string         `json:"timestamp,omitempty"`
}

type DiscordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

// alertKind names the kind of alert from its text, so the chat channels can head it with an emoji and a title
// without changing the messages, which are also the de-duplication keys.
func alertKind(message string) (emoji, kind string) {
	switch {
	case strings.HasPrefix(message, "proposal ") || strings.HasPrefix(message, "upgrade proposal "):
		return "🗳️", "Governance"
	case strings.HasPrefix(message, "upgrade "):
		return "⬆️", "Upgrade"
	case strings.HasPrefix(message, "voting power"):
		return "📊", "Voting power"
	case strings.HasPrefix(message, "RPC node "):
		return "📡", "RPC node down"
	case strings.HasPrefix(message, "no RPC endpoints"):
		return "📡", "No RPC endpoints"
	case strings.HasPrefix(message, "stalled"):
		return "⏸️", "Chain stalled"
	case strings.Contains(message, "has missed"):
		return "❌", "Missed blocks"
	case strings.Contains(message, "no longer active"):
		return "⚠️", "Validator inactive"
	}
	return "🚨", "Alert"
}

// alertHeading is the one-line title for a chat message: the kind, the chain, and whether it is a resolve.
func alertHeading(msg *alertMsg) string {
	emoji, kind := alertKind(msg.message)
	if msg.resolved {
		return "✅ Resolved: " + kind + " on " + msg.chain
	}
	return emoji + " " + kind + " on " + msg.chain
}

// alertBody breaks the one-line message into readable lines and drops the chain id, which the heading carries.
func alertBody(msg *alertMsg) string {
	body := strings.Replace(msg.message, " on "+msg.chainId, "", 1)
	for _, r := range []struct{ from, to string }{
		{" is open for voting until ", "\nopen for voting until "},
		{" closes in under ", "\ncloses in under "},
		{" and ", "\n"},
		{" has been down for ", "\ndown for "},
		{": have not seen", "\nhave not seen"},
	} {
		body = strings.Replace(body, r.from, r.to, 1)
	}
	return body
}

// severityColor is the embed colour for Discord and Slack.
func severityColor(msg *alertMsg) (discord uint, slack string) {
	switch {
	case msg.resolved:
		return 0x3CCF8E, "good"
	case msg.severity == "critical" || msg.severity == "error":
		return 0xE5534B, "danger"
	case msg.severity == "warning":
		return 0xF0A53A, "warning"
	}
	return 0x3B8BE0, "#3B8BE0"
}

func buildDiscordMessage(msg *alertMsg) *DiscordMessage {
	color, _ := severityColor(msg)
	fields := []DiscordField{{Name: "Chain", Value: msg.chainId, Inline: true}}
	if !msg.resolved {
		fields = append(fields, DiscordField{Name: "Severity", Value: msg.severity, Inline: true})
	}
	return &DiscordMessage{
		Username: "Tenderduty",
		Content:  msg.discMentions,
		Embeds: []DiscordEmbed{{
			Title:       alertHeading(msg),
			Description: alertBody(msg),
			Color:       color,
			Fields:      fields,
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
		}},
	}
}

func notifyTg(msg *alertMsg) (err error) {
	if !msg.tg {
		return nil
	}
	if !shouldNotify(msg, tg) {
		return nil
	}
	bot, err := tgbotapi.NewBotAPI(msg.tgKey)
	if err != nil {
		l("notify telegram:", err)
		return
	}

	text := alertHeading(msg) + "\n" + alertBody(msg) + "\n" + msg.chainId
	if msg.tgMentions != "" {
		text += "\n" + msg.tgMentions
	}
	mc := tgbotapi.NewMessageToChannel(msg.tgChannel, text)
	_, err = bot.Send(mc)
	if err != nil {
		l("telegram send:", err)
	}
	return err
}

func notifyPagerduty(msg *alertMsg) (err error) {
	if !msg.pd {
		return nil
	}
	if !shouldNotify(msg, pd) {
		return nil
	}
	// key from the example, don't spam their api
	if msg.key == "aaaaaaaaaaaabbbbbbbbbbbbbcccccccccccc" {
		l("invalid pagerduty key")
		return
	}
	action := "trigger"
	if msg.resolved {
		action = "resolve"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = pagerduty.ManageEventWithContext(ctx, pagerduty.V2Event{
		RoutingKey: msg.key,
		Action:     action,
		DedupKey:   msg.uniqueId,
		Payload: &pagerduty.V2Payload{
			Summary:  msg.message,
			Source:   msg.uniqueId,
			Severity: msg.severity,
		},
	})
	return
}

// listAlarms returns the active alarms for a chain, oldest first.
func listAlarms(chain string) []dash.AlertStatus {
	alarms.notifyMux.RLock()
	defer alarms.notifyMux.RUnlock()
	out := make([]dash.AlertStatus, 0, len(alarms.AllAlarms[chain]))
	for msg, since := range alarms.AllAlarms[chain] {
		out = append(out, dash.AlertStatus{Message: msg, Since: since.Unix()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since < out[j].Since })
	return out
}

// alert creates a universal alert and pushes it to the alertChan to be delivered to appropriate services
func (c *Config) alert(chainName, message, severity string, resolved bool, id *string) {
	c.chainsMux.RLock()
	uniq := c.Chains[chainName].ValAddress
	if id != nil {
		uniq = *id
	}
	if uniq == "" {
		uniq = c.Chains[chainName].ChainId
	}
	al := c.Chains[chainName].Alerts
	a := &alertMsg{
		pd:           c.Pagerduty.Enabled && al.Pagerduty.Enabled && wants(al.Pagerduty.SeverityThreshold, severity, resolved),
		disc:         c.Discord.Enabled && al.Discord.Enabled && wants(al.Discord.SeverityThreshold, severity, resolved),
		tg:           c.Telegram.Enabled && al.Telegram.Enabled && wants(al.Telegram.SeverityThreshold, severity, resolved),
		slk:          c.Slack.Enabled && al.Slack.Enabled && wants(al.Slack.SeverityThreshold, severity, resolved),
		severity:     severity,
		resolved:     resolved,
		chain:        chainName,
		chainId:      c.Chains[chainName].ChainId,
		message:      message,
		uniqueId:     uniq,
		key:          c.Chains[chainName].Alerts.Pagerduty.ApiKey,
		tgChannel:    c.Chains[chainName].Alerts.Telegram.Channel,
		tgKey:        c.Chains[chainName].Alerts.Telegram.ApiKey,
		tgMentions:   strings.Join(c.Chains[chainName].Alerts.Telegram.Mentions, " "),
		discHook:     c.Chains[chainName].Alerts.Discord.Webhook,
		discMentions: strings.Join(c.Chains[chainName].Alerts.Discord.Mentions, " "),
		slkHook:      c.Chains[chainName].Alerts.Slack.Webhook,
		slkMentions:  strings.Join(c.Chains[chainName].Alerts.Slack.Mentions, " "),
	}
	c.alertChan <- a
	c.chainsMux.RUnlock()
	alarms.notifyMux.Lock()
	defer alarms.notifyMux.Unlock()
	if alarms.AllAlarms[chainName] == nil {
		alarms.AllAlarms[chainName] = make(map[string]time.Time)
	}
	if resolved && !alarms.AllAlarms[chainName][message].IsZero() {
		delete(alarms.AllAlarms[chainName], message)
		return
	} else if resolved {
		return
	}
	alarms.AllAlarms[chainName][message] = time.Now()
}

// severityRank orders pagerduty severities so a channel threshold can be compared against an alert. Anything
// unrecognised ranks as critical so a typo never silences an alert.
func severityRank(s string) int {
	switch strings.ToLower(s) {
	case "info":
		return 0
	case "warning":
		return 1
	case "error":
		return 2
	}
	return 3
}

// wants reports whether a channel takes an alert of this severity. Resolves always go through, the dedup logic
// drops them when nothing was sent.
func wants(threshold, severity string, resolved bool) bool {
	return resolved || severityRank(severity) >= severityRank(threshold)
}

// alertEvent sends a one-off notice for something that already happened and has no resolved state, such as a
// change in voting power. It is not kept in the active alarm list.
func (c *Config) alertEvent(chainName, message, severity string, id *string) {
	c.alert(chainName, message, severity, false, id)
	alarms.forget(chainName, message)
}

// forget drops alarms from the dashboard without sending a resolve.
func (a *alarmCache) forget(chain string, messages ...string) {
	a.notifyMux.Lock()
	defer a.notifyMux.Unlock()
	for _, m := range messages {
		delete(a.AllAlarms[chain], m)
	}
}

// watch handles monitoring for missed blocks, stalled chain, node downtime
// and also updates a few prometheus stats
func (cc *ChainConfig) watch() {
	var missedAlarm, pctAlarm, noNodes bool
	inactive := "jailed"
	nodeAlarms := make(map[string]bool)

	// wait until we have a moniker:
	noNodesSec := 0 // delay a no-nodes alarm for 30 seconds, too noisy.
	for {
		if cc.valInfo == nil || cc.valInfo.Moniker == "not connected" {
			time.Sleep(time.Second)
			if cc.Alerts.AlertIfNoServers && !noNodes && cc.noNodes && noNodesSec >= 60*td.NodeDownMin {
				noNodes = true
				td.alert(
					cc.name,
					fmt.Sprintf("no RPC endpoints are working for %s", cc.ChainId),
					"critical",
					false,
					cc.alertId(""),
				)
			}
			noNodesSec += 1
			continue
		}
		noNodesSec = 0
		break
	}
	// initial stat creation for nodes, we only update again if the node is positive
	if td.Prom {
		for _, node := range cc.Nodes {
			td.statsChan <- cc.mkUpdate(metricNodeDownSeconds, 0, node.Url)
		}
	}

	for {
		time.Sleep(2 * time.Second)

		// alert if we can't monitor
		switch {
		case cc.Alerts.AlertIfNoServers && !noNodes && cc.noNodes:
			noNodesSec += 2
			if noNodesSec <= 30*td.NodeDownMin {
				if noNodesSec%20 == 0 {
					l(fmt.Sprintf("no nodes available on %s for %d seconds, deferring alarm", cc.ChainId, noNodesSec))
				}
				noNodes = false
			} else {
				noNodesSec = 0
				noNodes = true
				td.alert(
					cc.name,
					fmt.Sprintf("no RPC endpoints are working for %s", cc.ChainId),
					"critical",
					false,
					cc.alertId(""),
				)
			}
		case cc.Alerts.AlertIfNoServers && noNodes && !cc.noNodes:
			noNodes = false
			td.alert(
				cc.name,
				fmt.Sprintf("no RPC endpoints are working for %s", cc.ChainId),
				"critical",
				true,
				cc.alertId(""),
			)
		default:
			noNodesSec = 0
		}

		// stalled chain detection
		stalled := !cc.lastBlockTime.IsZero() &&
			cc.lastBlockTime.Before(time.Now().Add(time.Duration(-cc.Alerts.Stalled)*time.Minute))
		if cc.Alerts.StalledAlerts && stalled != cc.lastBlockAlarm {
			cc.lastBlockAlarm = stalled
			severity := "critical"
			if stalled {
				cc.stalledMsg = fmt.Sprintf("stalled: have not seen a new block on %s in %d minutes", cc.ChainId, cc.Alerts.Stalled)
				if cc.upgrade != nil && cc.upgrade.Height-cc.lastBlockNum <= 1 && cc.upgrade.Height >= cc.lastBlockNum {
					cc.stalledMsg += fmt.Sprintf(", the chain halted at upgrade height %d (%s) and needs the new binary", cc.upgrade.Height, cc.upgrade.Name)
				}
			} else {
				severity = "info"
			}
			td.alert(cc.name, cc.stalledMsg, severity, !stalled, cc.alertId(""))
		}

		// jailed detection - only alert if it changes.
		if cc.Alerts.AlertIfInactive && cc.lastValInfo != nil && cc.lastValInfo.Bonded != cc.valInfo.Bonded &&
			cc.lastValInfo.Moniker == cc.valInfo.Moniker {

			id := *cc.alertId("jailed")
			// just went inactive, figure out if it's jail or tombstone
			if !cc.valInfo.Bonded && cc.lastValInfo.Bonded {
				if cc.valInfo.Tombstoned {
					// don't worry about changing it back ... lol.
					inactive = "☠️ tombstoned 🪦"
				}
				td.alert(
					cc.name,
					fmt.Sprintf("%s is no longer active: validator is %s", cc.valInfo.Moniker, inactive),
					"critical",
					false,
					&id,
				)
			} else if cc.valInfo.Bonded && !cc.lastValInfo.Bonded {
				td.alert(
					cc.name,
					fmt.Sprintf("%s is no longer active: validator is %s", cc.valInfo.Moniker, inactive),
					"info",
					true,
					&id,
				)
			}
		}

		// consecutive missed block alarms:
		if !missedAlarm && cc.Alerts.ConsecutiveAlerts && int(cc.statConsecutiveMiss) >= cc.Alerts.ConsecutiveMissed {
			// alert on missed block counter!
			missedAlarm = true
			id := *cc.alertId("consecutive")
			td.alert(
				cc.name,
				fmt.Sprintf("%s has missed %d blocks on %s", cc.valInfo.Moniker, cc.Alerts.ConsecutiveMissed, cc.ChainId),
				cc.Alerts.ConsecutivePriority,
				false,
				&id,
			)
		} else if missedAlarm && int(cc.statConsecutiveMiss) < cc.Alerts.ConsecutiveMissed {
			// clear the alert
			missedAlarm = false
			id := *cc.alertId("consecutive")
			td.alert(
				cc.name,
				fmt.Sprintf("%s has missed %d blocks on %s", cc.valInfo.Moniker, cc.Alerts.ConsecutiveMissed, cc.ChainId),
				"info",
				true,
				&id,
			)
		}

		// window percentage missed block alarms
		if cc.Alerts.PercentageAlerts && !pctAlarm && 100*float64(cc.valInfo.Missed)/float64(cc.valInfo.Window) > float64(cc.Alerts.Window) {
			// alert on missed block counter!
			pctAlarm = true
			id := *cc.alertId("percent")
			td.alert(
				cc.name,
				fmt.Sprintf("%s has missed > %d%% of the slashing window's blocks on %s", cc.valInfo.Moniker, cc.Alerts.Window, cc.ChainId),
				cc.Alerts.PercentagePriority,
				false,
				&id,
			)
		} else if cc.Alerts.PercentageAlerts && pctAlarm && 100*float64(cc.valInfo.Missed)/float64(cc.valInfo.Window) < float64(cc.Alerts.Window) {
			// clear the alert
			pctAlarm = false
			id := *cc.alertId("percent")
			td.alert(
				cc.name,
				fmt.Sprintf("%s has missed > %d%% of the slashing window's blocks on %s", cc.valInfo.Moniker, cc.Alerts.Window, cc.ChainId),
				"info",
				true,
				&id,
			)
		}

		// node down alarms
		for _, node := range cc.Nodes {
			// window percentage missed block alarms
			if node.AlertIfDown && node.down && !node.wasDown && !node.downSince.IsZero() &&
				time.Since(node.downSince) > time.Duration(td.NodeDownMin)*time.Minute {
				// alert on dead node
				if !nodeAlarms[node.Url] {
				} else {
					continue
				}
				nodeAlarms[node.Url] = true // used to keep active alert count correct
				td.alert(
					cc.name,
					fmt.Sprintf("RPC node %s has been down for > %d minutes on %s", node.Url, td.NodeDownMin, cc.ChainId),
					td.NodeDownSeverity,
					false,
					&node.Url,
				)
			} else if node.AlertIfDown && !node.down && node.wasDown {
				// clear the alert
				nodeAlarms[node.Url] = false
				node.wasDown = false
				td.alert(
					cc.name,
					fmt.Sprintf("RPC node %s has been down for > %d minutes on %s", node.Url, td.NodeDownMin, cc.ChainId),
					"info",
					true,
					&node.Url,
				)
			}
		}

		if td.Prom {
			// raw block timer, ignoring finalized state
			td.statsChan <- cc.mkUpdate(metricLastBlockSecondsNotFinal, time.Since(cc.lastBlockTime).Seconds(), "")
			// update node-down times for prometheus
			for _, node := range cc.Nodes {
				if node.down && !node.downSince.IsZero() {
					td.statsChan <- cc.mkUpdate(metricNodeDownSeconds, time.Since(node.downSince).Seconds(), node.Url)
				}
			}
		}
	}
}
