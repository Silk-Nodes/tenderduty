package tenderduty

import (
	"context"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

var (
	promMux sync.RWMutex
)

type metricType uint8

const (
	metricSigned metricType = iota
	metricProposed
	metricMissed
	metricPrevote
	metricPrecommit
	metricConsecutive
	metricWindowMissed
	metricWindowSize
	metricLastBlockSeconds
	metricLastBlockSecondsNotFinal

	metricTotalNodes
	metricUnealthyNodes
	metricNodeLagSeconds
	metricNodeDownSeconds

	metricUpgradeBlocksRemaining
	metricUnvotedProposals
	metricVotingPowerTokens
	metricVotingPowerPercent
)

type promUpdate struct {
	metric   metricType
	counter  float64
	name     string
	chainId  string
	moniker  string
	endpoint string
}

type metrics map[metricType]*prometheus.GaugeVec

func (m metrics) setStat(update *promUpdate) {
	if update.moniker == "not connected" {
		// the validator is not known yet, a sample now would create a second label set that never updates again
		return
	}
	lbls := map[string]string{
		"name":     update.name,
		"chain_id": update.chainId,
		"moniker":  update.moniker,
	}
	promMux.RLock()
	defer promMux.RUnlock()
	if update.metric == metricNodeLagSeconds || update.metric == metricNodeDownSeconds {
		lbls["endpoint"] = update.endpoint
	}
	// GetMetricWith returns an error for a label a node made up (invalid UTF-8) where With would panic
	g, err := m[update.metric].GetMetricWith(lbls)
	if err != nil {
		l(fmt.Sprintf("prometheus: dropping update for %s: %v", update.chainId, err))
		return
	}
	g.Set(update.counter)
}

func prometheusExporter(ctx context.Context, updates chan *promUpdate) {
	// attributes used to uniquely identify each chain
	var chainLabels = []string{"name", "chain_id", "moniker"}
	var hostLabels = []string{"name", "chain_id", "moniker", "endpoint"}

	// setup our signing gauges
	signed := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_signed_blocks",
		Help: "count of blocks signed since tenderduty was started",
	}, chainLabels)
	proposed := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_proposed_blocks",
		Help: "count of blocks proposed since tenderduty was started",
	}, chainLabels)
	missed := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_missed_blocks",
		Help: "count of blocks missed without seeing a precommit or prevote since tenderduty was started",
	}, chainLabels)
	missedPrevote := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_missed_blocks_prevote_present",
		Help: "count of blocks missed where a prevote was seen since tenderduty was started",
	}, chainLabels)
	missedPrecommit := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_missed_blocks_precommit_present",
		Help: "count of blocks missed where a precommit was seen since tenderduty was started",
	}, chainLabels)
	missedConsecutive := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_consecutive_missed_blocks",
		Help: "the current count of consecutively missed blocks regardless of precommit or prevote status",
	}, chainLabels)
	windowSize := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_missed_block_window",
		Help: "the missed block aka slashing window",
	}, chainLabels)
	missedWindow := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_missed_blocks_for_window",
		Help: "the current count of missed blocks in the slashing window regardless of precommit or prevote status",
	}, chainLabels)
	lastBlockSec := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_time_since_last_block",
		Help: "how many seconds since the previous block was finalized, only set when a new block is seen, not useful for stall detection, helpful for averaging times",
	}, chainLabels)
	lastBlockSecUnfinalized := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_time_since_last_block_unfinalized",
		Help: "how many seconds since the previous block was finalized, set regardless of finalization, useful for stall detection, not helpful for figuring average time",
	}, chainLabels)

	// setup node health gauges:
	nodesMonitored := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_total_monitored_endpoints",
		Help: "the count of rpc endpoints being monitored for a chain",
	}, chainLabels)
	nodesUnhealthy := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_total_unhealthy_endpoints",
		Help: "the count of unhealthy rpc endpoints being monitored for a chain",
	}, chainLabels)

	// extra labels for individual node stats
	nodeLagSec := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_endpoint_syncing_seconds_behind",
		Help: "how many seconds a node is behind the head of a chain",
	}, hostLabels)
	nodeDownSec := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_endpoint_down_seconds",
		Help: "how many seconds a node has been marked as unhealthy",
	}, hostLabels)

	upgradeBlocks := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_upgrade_blocks_remaining",
		Help: "blocks until the scheduled x/upgrade height, -1 when no upgrade is scheduled",
	}, chainLabels)
	unvotedProposals := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_unvoted_proposals",
		Help: "count of proposals in their voting period that the validator has not voted on",
	}, chainLabels)
	votingPowerTokens := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_voting_power_tokens",
		Help: "stake delegated to the validator in the chain's base denomination",
	}, chainLabels)
	votingPowerPercent := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tenderduty_voting_power_percent",
		Help: "the validator's share of all bonded tokens on the chain",
	}, chainLabels)

	m := metrics{
		metricSigned:                   signed,
		metricProposed:                 proposed,
		metricMissed:                   missed,
		metricPrevote:                  missedPrevote,
		metricPrecommit:                missedPrecommit,
		metricConsecutive:              missedConsecutive,
		metricWindowMissed:             missedWindow,
		metricWindowSize:               windowSize,
		metricLastBlockSeconds:         lastBlockSec,
		metricLastBlockSecondsNotFinal: lastBlockSecUnfinalized,
		metricTotalNodes:               nodesMonitored,
		metricUnealthyNodes:            nodesUnhealthy,
		metricNodeLagSeconds:           nodeLagSec,
		metricNodeDownSeconds:          nodeDownSec,
		metricUpgradeBlocksRemaining:   upgradeBlocks,
		metricUnvotedProposals:         unvotedProposals,
		metricVotingPowerTokens:        votingPowerTokens,
		metricVotingPowerPercent:       votingPowerPercent,
	}

	go guard("prometheus exporter", func() {
		for {
			select {
			case u := <-updates:
				m.setStat(u)
			case <-ctx.Done():
				return
			}
		}
	})

	promMux := http.NewServeMux()

	addr := net.JoinHostPort(td.ListenAddress, strconv.Itoa(td.PrometheusListenPort))
	l(fmt.Sprintf("serving prometheus metrics at %s/metrics", addr))
	promMux.Handle("/metrics", promhttp.Handler())
	promSrv := &http.Server{
		Addr:              addr,
		Handler:           promMux,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 20 * time.Second,
	}
	log.Fatal(promSrv.ListenAndServe())
}
