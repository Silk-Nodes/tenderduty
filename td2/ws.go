package tenderduty

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	pbtypes "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/gorilla/websocket"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	QueryNewBlock string = `tm.event='NewBlock'`
	QueryVote     string = `tm.event='Vote'`
)

// StatusType represents the various possible end states. Prevote and Precommit are special cases, where the node
// monitoring for misses did see them, but the proposer did not include in the block.
type StatusType int

const (
	Statusmissed StatusType = iota
	StatusPrevote
	StatusPrecommit
	StatusSigned
	StatusProposed
)

// StatusUpdate is passed over a channel from the websocket client indicating the current state, it is immediate in the
// case of prevotes etc, and the highest value seen is used in the final determination (which is how we tag
// prevote/precommit + missed blocks.
type StatusUpdate struct {
	Height int64
	Status StatusType
	Final  bool
}

// WsReply is a trimmed down version of the JSON sent from a tendermint websocket subscription.
type WsReply struct {
	Id     int64 `json:"id"`
	Result struct {
		Query string `json:"query"`
		Data  struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"data"`
	} `json:"result"`
}

// Type is the abci message type
func (wsr WsReply) Type() string {
	return wsr.Result.Data.Type
}

// Value returns the JSON encoded raw bytes from the response. Unlike an ABCI RPC query, these are not protobuf.
func (wsr WsReply) Value() []byte {
	if wsr.Result.Data.Value == nil {
		return make([]byte, 0)
	}
	return wsr.Result.Data.Value
}

// WsRun is our main entrypoint for the websocket listener. In the Run loop it will block, and if it exits force a
// renegotiation for a new client.
func (cc *ChainConfig) WsRun() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var err error
	started := time.Now()
	for {
		// wait until our RPC client is connected and running. We will use the same URL for the websocket
		if cc.client == nil || cc.valInfo == nil || (cc.valInfo.Conspub == nil && !cc.rpcOnly()) {
			if started.Before(time.Now().Add(-2 * time.Minute)) {
				l(cc.name, "websocket client timed out waiting for a working rpc endpoint, restarting")
				return
			}
			l("⏰ waiting for a healthy client for", cc.ChainId)
			time.Sleep(30 * time.Second)
			continue
		}
		break
	}

	ws, err := newWsClient(cc.currentUrl)
	if err != nil {
		l(err)
		cancel()
		return
	}
	cc.clientMux.Lock()
	cc.wsclient = ws
	cc.clientMux.Unlock()
	defer ws.Close()
	err = ws.SetCompressionLevel(3)
	if err != nil {
		log.Println(err)
	}

	// This go func processes the results returned by the listeners. It has most of the logic on where data is sent,
	// like dashboards or prometheus.
	resultChan := make(chan StatusUpdate)
	go func() {
		var signState StatusType = -1
		for {
			select {
			case update := <-resultChan:
				if update.Final && update.Height%20 == 0 {
					l(fmt.Sprintf("🧊 %-12s block %d", cc.ChainId, update.Height))
				}
				if update.Status > signState && cc.valInfo.Bonded {
					signState = update.Status
				}
				if update.Final {
					cc.lastBlockNum = update.Height
					if !cc.lastBlockTime.IsZero() {
						if td.Prom {
							td.statsChan <- cc.mkUpdate(metricLastBlockSeconds, time.Since(cc.lastBlockTime).Seconds(), "")
						}
						// smoothed block interval, used to estimate when an upgrade height will arrive
						if d := time.Since(cc.lastBlockTime).Seconds(); d < 60 {
							if cc.avgBlockTime == 0 {
								cc.avgBlockTime = d
							} else {
								cc.avgBlockTime = 0.9*cc.avgBlockTime + 0.1*d
							}
						}
					}
					cc.lastBlockTime = time.Now()
					cc.blocksResults = append([]int{int(signState)}, cc.blocksResults[:len(cc.blocksResults)-1]...)
					if signState < 3 && cc.valInfo.Bonded {
						warn := fmt.Sprintf("❌ warning      %s missed block %d on %s", cc.valInfo.Moniker, update.Height, cc.ChainId)
						cc.lastError = time.Now().UTC().Format(time.RFC3339) + " " + warn
						l(warn)
					}
					switch signState {
					case Statusmissed:
						cc.statTotalMiss += 1
						cc.statConsecutiveMiss += 1
					case StatusPrecommit:
						cc.statPrecommitMiss += 1
						cc.statTotalMiss += 1
						cc.statConsecutiveMiss += 1
					case StatusPrevote:
						cc.statPrevoteMiss += 1
						cc.statTotalMiss += 1
						cc.statConsecutiveMiss += 1
					case StatusSigned:
						cc.statTotalSigns += 1
						cc.statConsecutiveMiss = 0
					case StatusProposed:
						cc.statTotalProps += 1
						cc.statTotalSigns += 1
						cc.statConsecutiveMiss = 0
					}
					signState = -1
					cc.pushStatus()

					if td.Prom {
						td.statsChan <- cc.mkUpdate(metricSigned, cc.statTotalSigns, "")
						td.statsChan <- cc.mkUpdate(metricProposed, cc.statTotalProps, "")
						td.statsChan <- cc.mkUpdate(metricMissed, cc.statTotalMiss, "")
						td.statsChan <- cc.mkUpdate(metricPrevote, cc.statPrevoteMiss, "")
						td.statsChan <- cc.mkUpdate(metricPrecommit, cc.statPrecommitMiss, "")
						td.statsChan <- cc.mkUpdate(metricConsecutive, cc.statConsecutiveMiss, "")
						td.statsChan <- cc.mkUpdate(metricUnealthyNodes, float64(len(cc.Nodes)-cc.healthyNodes()), "")
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	voteChan := make(chan *WsReply)
	go handleVotes(ctx, voteChan, resultChan, strings.ToUpper(hex.EncodeToString(cc.valInfo.Conspub)))

	blockChan := make(chan *WsReply)
	go func() {
		e := handleBlocks(ctx, blockChan, resultChan, strings.ToUpper(hex.EncodeToString(cc.valInfo.Conspub)))
		if e != nil {
			l("🛑", cc.ChainId, e)
			cancel()
		}
	}()

	// now that channel consumers are up, create our subscriptions and route data.
	go func() {
		var msg []byte
		var e error
		for {
			_, msg, e = ws.ReadMessage()
			if e != nil {
				l(e)
				cancel()
				return
			}
			reply := &WsReply{}
			e = json.Unmarshal(msg, reply)
			if e != nil {
				continue
			}
			switch reply.Type() {
			case `tendermint/event/NewBlock`:
				blockChan <- reply
			case `tendermint/event/Vote`:
				voteChan <- reply
			default:
				// fmt.Println("unknown response", reply.Type())
			}
		}
	}()

	subscriptions := []string{QueryNewBlock, QueryVote}
	if cc.rpcOnly() {
		// without a validator there is nothing to learn from votes, blocks alone give height and timing
		subscriptions = subscriptions[:1]
	}
	for _, subscribe := range subscriptions {
		q := fmt.Sprintf(`{"jsonrpc":"2.0","method":"subscribe","id":1,"params":{"query":"%s"}}`, subscribe)
		err = ws.WriteMessage(websocket.TextMessage, []byte(q))
		if err != nil {
			l(err)
			cancel()
			break
		}
	}
	l(fmt.Sprintf("⚙️ %-12s watching for %s via %s", cc.ChainId, strings.Join(subscriptions, " and "), cc.currentUrl))
	for {
		select {
		case <-cc.client.Quit():
			cancel()
		case <-ctx.Done():
			return
		}
	}
}

type stringInt64 string

// helper to make the "everything is a string" issue less painful.
func (si stringInt64) val() int64 {
	i, _ := strconv.ParseInt(string(si), 10, 64)
	return i
}

type signature struct {
	ValidatorAddress string `json:"validator_address"`
}

// rawBlock is a trimmed down version of the block subscription result, it contains only what we need.
type rawBlock struct {
	Block struct {
		Header struct {
			Height          stringInt64 `json:"height"`
			ProposerAddress string      `json:"proposer_address"`
		} `json:"header"`
		LastCommit struct {
			Signatures []signature `json:"signatures"`
		} `json:"last_commit"`
	} `json:"block"`
}

// find determines if a validator's pre-commit was included in a finalized block.
func (rb rawBlock) find(val string) bool {
	if rb.Block.LastCommit.Signatures == nil {
		return false
	}
	for _, v := range rb.Block.LastCommit.Signatures {
		if v.ValidatorAddress == val {
			return true
		}
	}
	return false
}

// handleBlocks consumes the channel for new blocks and when it sees one sends a status update. It's also
// responsible for stalled chain detection and will shutdown the client if there are no blocks for a minute.
func handleBlocks(ctx context.Context, blocks chan *WsReply, results chan StatusUpdate, address string) error {
	live := time.NewTicker(time.Minute)
	defer live.Stop()
	lastBlock := time.Now()
	for {
		select {
		case <-live.C:
			// no block for a full minute likely means we have either a dead chain, or a dead client.
			if lastBlock.Before(time.Now().Add(-time.Minute)) {
				return errors.New("websocket idle for 1 minute, exiting")
			}
		case block := <-blocks:
			lastBlock = time.Now()
			b := &rawBlock{}
			err := json.Unmarshal(block.Value(), b)
			if err != nil {
				l("could not decode block", err)
				continue
			}
			upd := StatusUpdate{
				Height: b.Block.Header.Height.val(),
				Status: Statusmissed,
				Final:  true,
			}
			if b.Block.Header.ProposerAddress == address {
				upd.Status = StatusProposed
			} else if b.find(address) {
				upd.Status = StatusSigned
			}
			results <- upd
		case <-ctx.Done():
			return nil
		}
	}
}

// rawVote is a trimmed down version of the vote response.
type rawVote struct {
	Vote struct {
		Type             pbtypes.SignedMsgType `json:"type"`
		Height           stringInt64           `json:"height"`
		ValidatorAddress string                `json:"validator_address"`
	} `json:"Vote"`
}

// handleVotes consumes the channel for precommits and prevotes, tracking where in the process a validator is.
func handleVotes(ctx context.Context, votes chan *WsReply, results chan StatusUpdate, address string) {
	for {
		select {
		case reply := <-votes:
			vote := &rawVote{}
			err := json.Unmarshal(reply.Value(), vote)
			if err != nil {
				l(err)
				continue
			}
			if vote.Vote.ValidatorAddress == address {
				upd := StatusUpdate{Height: vote.Vote.Height.val()}
				switch vote.Vote.Type.String() {
				case "":
					continue
				case "SIGNED_MSG_TYPE_PREVOTE":
					upd.Status = StatusPrevote
				case "SIGNED_MSG_TYPE_PRECOMMIT":
					upd.Status = StatusPrecommit
				case "SIGNED_MSG_TYPE_PROPOSAL":
					upd.Status = StatusProposed
				}
				results <- upd
			}

		case <-ctx.Done():
			return
		}
	}
}

// newWsClient dials the websocket endpoint of an RPC node. Public nodes sometimes serve RPC under a path prefix,
// so the /websocket suffix is only added when missing.
func newWsClient(u string) (*websocket.Conn, error) {
	u = strings.TrimRight(u, "/")
	if !strings.HasSuffix(u, "/websocket") {
		u += "/websocket"
	}
	endpoint, err := url.Parse(u)
	if err != nil {
		return nil, fmt.Errorf("parsing websocket url %s: %w", u, err)
	}
	switch endpoint.Scheme {
	case "http", "tcp", "ws":
		endpoint.Scheme = "ws"
	case "https", "wss":
		endpoint.Scheme = "wss"
	default:
		return nil, fmt.Errorf("protocol %s is unknown, valid choices are http, https, tcp, ws, and wss", endpoint.Scheme)
	}
	conn, _, err := websocket.DefaultDialer.Dial(endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("could not dial websocket %s: %w", endpoint.String(), err)
	}
	return conn, nil
}
