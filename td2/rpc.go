package tenderduty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
)

const (
	probeTimeout = 10 * time.Second
	// wsFailPenalty is how long a node is passed over after its websocket stopped delivering blocks.
	wsFailPenalty = 10 * time.Minute
)

var errSyncing = errors.New("not synced")

// probeResult is what one node reported when asked for its status.
type probeResult struct {
	node      *NodeConfig
	height    int64
	blockTime time.Time
	err       error
}

// probe asks a node for its status and reports how far along it is. Each call has its own timeout so one
// unresponsive node cannot eat into the time budget of the others.
func probe(u, chainID string) (height int64, blockTime time.Time, err error) {
	if _, err = url.Parse(u); err != nil {
		return 0, time.Time{}, fmt.Errorf("could not parse url: %w", err)
	}
	c, err := rpchttp.New(u, "/websocket")
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("could not create client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	status, err := c.Status(ctx)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("down: %w", err)
	}
	if status.NodeInfo.Network != chainID {
		return 0, time.Time{}, fmt.Errorf("on the wrong network: %s, expected %s", status.NodeInfo.Network, chainID)
	}
	if status.SyncInfo.CatchingUp {
		return 0, time.Time{}, errSyncing
	}
	return status.SyncInfo.LatestBlockHeight, status.SyncInfo.LatestBlockTime, nil
}

// probeAll checks every configured node at the same time.
func (cc *ChainConfig) probeAll() []probeResult {
	results := make([]probeResult, len(cc.Nodes))
	var wg sync.WaitGroup
	for i, node := range cc.Nodes {
		wg.Add(1)
		go func(i int, node *NodeConfig) {
			defer wg.Done()
			h, t, err := probe(node.Url, cc.ChainId)
			results[i] = probeResult{node: node, height: h, blockTime: t, err: err}
		}(i, node)
	}
	wg.Wait()
	return results
}

// best is the highest height and newest block time any reachable node reported.
func best(results []probeResult) (height int64, blockTime time.Time) {
	for _, r := range results {
		if r.err != nil {
			continue
		}
		if r.height > height {
			height = r.height
		}
		if r.blockTime.After(blockTime) {
			blockTime = r.blockTime
		}
	}
	return
}

// pickNode chooses which node to monitor from. The highest block height wins and ties go to config order.
// Nodes that errored or sit more than maxLag blocks behind are skipped. Nodes whose websocket failed recently
// are only chosen when nothing else is reachable, so a node that answers status calls but delivers no blocks
// is not selected over and over.
func pickNode(results []probeResult, maxLag int64, now time.Time) int {
	top, _ := best(results)
	pick, pickHeight, pickPenalised := -1, int64(-1), true
	for i, r := range results {
		if r.err != nil || top-r.height > maxLag {
			continue
		}
		penalised := now.Sub(r.node.wsFailedAt) < wsFailPenalty
		if pick == -1 || (pickPenalised && !penalised) || (penalised == pickPenalised && r.height > pickHeight) {
			pick, pickHeight, pickPenalised = i, r.height, penalised
		}
	}
	return pick
}

// applyProbes records each node's probe outcome and returns the index of the node to use, or -1.
func (cc *ChainConfig) applyProbes(results []probeResult) int {
	top, topTime := best(results)
	for _, r := range results {
		node := r.node
		node.height, node.lag = r.height, 0
		node.syncing = errors.Is(r.err, errSyncing)
		switch {
		case r.err != nil:
			cc.markDown(node, r.err.Error())
		case top-r.height > int64(td.LagBlocks):
			node.lag = top - r.height
			cc.markDown(node, fmt.Sprintf("lagging %d blocks behind", node.lag))
		default:
			node.lag = top - r.height
			cc.markUp(node)
		}
		if td.Prom && r.err == nil {
			td.statsChan <- cc.mkUpdate(metricNodeLagSeconds, topTime.Sub(r.blockTime).Seconds(), node.Url)
		}
	}
	return pickNode(results, int64(td.LagBlocks), time.Now())
}

// markDown records that a node cannot be used and why. The transition is logged once, later probes only
// refresh the message.
func (cc *ChainConfig) markDown(node *NodeConfig, msg string) {
	node.lastMsg = msg
	if !node.down {
		node.down = true
		node.downSince = time.Now()
		l(fmt.Sprintf("⚠️ %-12s node %s is %s", cc.name, node.Url, msg))
	}
	if td.Prom {
		td.statsChan <- cc.mkUpdate(metricNodeDownSeconds, time.Since(node.downSince).Seconds(), node.Url)
	}
}

// markUp records that a node answered correctly.
func (cc *ChainConfig) markUp(node *NodeConfig) {
	if node.down {
		node.wasDown = true
		l(fmt.Sprintf("🟢 %-12s node %s is healthy again", cc.name, node.Url))
	}
	node.down, node.lastMsg, node.downSince = false, "", time.Time{}
	if td.Prom {
		td.statsChan <- cc.mkUpdate(metricNodeDownSeconds, 0, node.Url)
	}
}

// useNode points the RPC client at a node.
func (cc *ChainConfig) useNode(u string) error {
	c, err := rpchttp.New(u, "/websocket")
	if err != nil {
		return err
	}
	cc.clientMux.Lock()
	cc.client = c
	cc.currentUrl = u
	cc.clientMux.Unlock()
	cc.noNodes = false
	cc.lastError = ""
	return nil
}

// currentNode is the configured node the RPC client is pointed at, nil when none or when using the public fallback.
func (cc *ChainConfig) currentNode() *NodeConfig {
	for _, n := range cc.Nodes {
		if n.Url == cc.currentUrl {
			return n
		}
	}
	return nil
}

// closeWs drops the websocket so the run loop reconnects through newRpc.
func (cc *ChainConfig) closeWs() {
	cc.clientMux.Lock()
	defer cc.clientMux.Unlock()
	if cc.wsclient != nil {
		_ = cc.wsclient.Close()
	}
}

// newRpc points the RPC client at the best node available. Every node is probed at once and the one with the
// highest block height wins.
func (cc *ChainConfig) newRpc() error {
	if i := cc.applyProbes(cc.probeAll()); i >= 0 {
		return cc.useNode(cc.Nodes[i].Url)
	}
	if cc.PublicFallback {
		if u, ok := getRegistryUrl(cc.ChainId); ok {
			node := guessPublicEndpoint(u)
			l(cc.ChainId, "⛑ attempting to use public fallback node", node)
			if _, _, err := probe(node, cc.ChainId); err == nil {
				l(cc.ChainId, "⛑ connected to public endpoint", node)
				return cc.useNode(node)
			}
		} else {
			l("could not find a public endpoint for", cc.ChainId)
		}
	}
	cc.noNodes = true
	cc.lastError = "no usable RPC endpoints available for " + cc.ChainId
	cc.pushStatus()
	return errors.New("no usable endpoints available for " + cc.ChainId)
}

// monitorHealth probes every node once a minute, refreshes the validator's state, and moves the websocket off
// the node in use when it is down or lagging while another node is healthy.
func (cc *ChainConfig) monitorHealth(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		// the run loop does the first probe and validator lookup itself, so this waits a tick to avoid racing it
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if i := cc.applyProbes(cc.probeAll()); i >= 0 {
			current := cc.currentNode()
			switch {
			case current != nil && current.down:
				l(fmt.Sprintf("🔀 %-12s moving from %s to %s", cc.name, current.Url, cc.Nodes[i].Url))
				cc.closeWs()
			case current == nil && cc.currentUrl != "":
				// running on the public fallback and one of our own nodes is back
				l(fmt.Sprintf("🔀 %-12s moving from public fallback %s back to %s", cc.name, cc.currentUrl, cc.Nodes[i].Url))
				cc.closeWs()
			}
		}
		if cc.client != nil {
			if !cc.rpcOnly() {
				if cc.valInfo != nil {
					v := *cc.valInfo
					cc.lastValInfo = &v
				}
				if err := cc.GetValInfo(false); err != nil {
					l("❓ refreshing signing info for", cc.ValAddress, err)
				}
			}
			cc.refreshGov()
			cc.alertUpgrade()
			cc.alertGov()
			cc.alertVotingPower()
		}
		cc.pushStatus()
	}
}

func (c *Config) pingHealthcheck() {
	if !c.Healthcheck.Enabled {
		return
	}
	ticker := time.NewTicker(c.Healthcheck.PingRate * time.Second)
	client := &http.Client{Timeout: 10 * time.Second}
	go func() {
		for range ticker.C {
			resp, err := client.Get(c.Healthcheck.PingURL)
			if err != nil {
				l(fmt.Sprintf("❌ Failed to ping healthcheck URL: %s", err.Error()))
				continue
			}
			_ = resp.Body.Close()
			l(fmt.Sprintf("🏓 Successfully pinged healthcheck URL: %s", c.Healthcheck.PingURL))
		}
	}()
}

// endpointRex matches the first a tag's hostname and port if present.
var endpointRex = regexp.MustCompile(`//([^/:]+)(:\d+)?`)

// guessPublicEndpoint attempts to deal with a shortcoming in the tendermint RPC client that doesn't allow path prefixes.
// The cosmos.directory requires them. This is a workaround to get the actual URL for the server behind their proxy.
// The RPC base URL will return links endpoints, and we can parse this to guess the original URL.
func guessPublicEndpoint(u string) string {
	resp, err := http.Get(u + "/")
	if err != nil {
		return u
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return u
	}
	_ = resp.Body.Close()
	matches := endpointRex.FindStringSubmatch(string(b))
	if len(matches) < 2 {
		// didn't work
		return u
	}
	proto := "https://"
	port := ":443"
	// will be 3 elements if there is a port no port means listening on https
	if len(matches) == 3 && matches[2] != "" && matches[2] != ":443" {
		proto = "http://"
		port = matches[2]
	}
	return proto + matches[1] + port
}
