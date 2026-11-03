package tenderduty

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"cosmossdk.io/math"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	slashing "github.com/cosmos/cosmos-sdk/x/slashing/types"
	staking "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// ValInfo holds most of the stats/info used for secondary alarms. It is refreshed roughly every minute.
type ValInfo struct {
	Moniker        string  `json:"moniker"`
	Bonded         bool    `json:"bonded"`
	Jailed         bool    `json:"jailed"`
	Tombstoned     bool    `json:"tombstoned"`
	Missed         int64   `json:"missed"`
	Window         int64   `json:"window"`
	JailAfter      int64   `json:"jail_after"` // missed blocks in the window beyond which the chain jails
	Conspub        []byte  `json:"conspub"`
	Valcons        string  `json:"valcons"`
	Tokens         float64 `json:"tokens"`           // stake delegated to the validator, in the base denomination
	VotingPowerPct float64 `json:"voting_power_pct"` // share of all bonded tokens on the chain
}

// abciError is a non-zero response code from the application.
type abciError struct {
	code uint32
	log  string
}

func (e *abciError) Error() string { return fmt.Sprintf("abci code %d: %s", e.code, e.log) }

// abciQuery sends a protobuf request to the application through the RPC node and decodes the reply. A non-zero
// response code comes back as an abciError so callers can tell "not found" from a dead node.
func abciQuery(ctx context.Context, client *rpchttp.HTTP, path string, req interface{ Marshal() ([]byte, error) }, resp interface{ Unmarshal([]byte) error }) error {
	if client == nil {
		return errors.New("nil rpc client")
	}
	b, err := req.Marshal()
	if err != nil {
		return err
	}
	r, err := client.ABCIQuery(ctx, path, b)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if r.Response.Code != 0 {
		return fmt.Errorf("%s: %w", path, &abciError{code: r.Response.Code, log: r.Response.Log})
	}
	return resp.Unmarshal(r.Response.Value)
}

// isAbciError reports whether the application, rather than the transport, rejected the query.
func isAbciError(err error) bool {
	var ae *abciError
	return errors.As(err, &ae)
}

// abciLogContains reports whether err is an application error whose log mentions s.
func abciLogContains(err error, s string) bool {
	var ae *abciError
	return errors.As(err, &ae) && strings.Contains(strings.ToLower(ae.log), s)
}

// toFloat converts a chain integer to a float for display and percentage maths.
func toFloat(i math.Int) float64 {
	if i.IsNil() {
		return 0
	}
	f, _ := new(big.Float).SetInt(i.BigInt()).Float64()
	return f
}

// GetValInfo the first bool is used to determine if extra information about the validator should be printed.
func (cc *ChainConfig) GetValInfo(first bool) (err error) {
	if cc.client == nil {
		return errors.New("nil rpc client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if cc.valInfo == nil {
		cc.valInfo = &ValInfo{}
	}

	// Fetch info from /cosmos.staking.v1beta1.Query/Validator
	// it's easier to ask people to provide valoper since it's readily available on
	// explorers, so make it easy and lookup the consensus key for them.
	cc.valInfo.Conspub, cc.valInfo.Moniker, cc.valInfo.Jailed, cc.valInfo.Bonded, cc.valInfo.Tokens, err = getVal(ctx, cc.client, cc.ValAddress)
	if err != nil {
		return
	}
	if first && cc.valInfo.Bonded {
		l(fmt.Sprintf("⚙️ found %s (%s) in validator set", cc.ValAddress, cc.valInfo.Moniker))
	} else if first && !cc.valInfo.Bonded {
		l(fmt.Sprintf("❌ %s (%s) is INACTIVE", cc.ValAddress, cc.valInfo.Moniker))
	}

	// share of the bonded pool, shown on the dashboard and used by the voting power alert
	pool := &staking.QueryPoolResponse{}
	if e := abciQuery(ctx, cc.client, "/cosmos.staking.v1beta1.Query/Pool", &staking.QueryPoolRequest{}, pool); e == nil {
		if bonded := toFloat(pool.Pool.BondedTokens); bonded > 0 {
			cc.valInfo.VotingPowerPct = 100 * cc.valInfo.Tokens / bonded
		}
	}

	if strings.Contains(cc.ValAddress, "valcons") {
		// no need to change prefix for signing info query
		cc.valInfo.Valcons = cc.ValAddress
	} else {
		// the consensus address uses the chain's account prefix plus "valcons"
		hrp, _, e := bech32.DecodeAndConvert(cc.ValAddress)
		if e != nil {
			return fmt.Errorf("could not decode valoper address %s: %w", cc.ValAddress, e)
		}
		cc.valInfo.Valcons, err = bech32.ConvertAndEncode(consensusPrefix(hrp), cc.valInfo.Conspub[:20])
		if err != nil {
			return
		}
		if first {
			l("⚙️", cc.ValAddress[:20], "... is using consensus key:", cc.valInfo.Valcons)
		}
	}

	// get current signing information (tombstoned, missed block count)
	slash := &slashing.QuerySigningInfoResponse{}
	err = abciQuery(ctx, cc.client, "/cosmos.slashing.v1beta1.Query/SigningInfo", &slashing.QuerySigningInfoRequest{ConsAddress: cc.valInfo.Valcons}, slash)
	if err != nil {
		return fmt.Errorf("could not query validator slashing status: %w", err)
	}
	cc.valInfo.Tombstoned = slash.ValSigningInfo.Tombstoned
	if cc.valInfo.Tombstoned {
		l(fmt.Sprintf("❗️☠️ %s (%s) is tombstoned 🪦❗️", cc.ValAddress, cc.valInfo.Moniker))
	}
	cc.valInfo.Missed = slash.ValSigningInfo.MissedBlocksCounter
	if td.Prom {
		td.statsChan <- cc.mkUpdate(metricWindowMissed, float64(cc.valInfo.Missed), "")
	}

	// finally get the signed blocks window
	if cc.valInfo.Window == 0 {
		params := &slashing.QueryParamsResponse{}
		err = abciQuery(ctx, cc.client, "/cosmos.slashing.v1beta1.Query/Params", &slashing.QueryParamsRequest{}, params)
		if err != nil {
			return fmt.Errorf("🛑 could not query slashing params: %w", err)
		}
		if first && td.Prom {
			td.statsChan <- cc.mkUpdate(metricWindowSize, float64(params.Params.SignedBlocksWindow), "")
			td.statsChan <- cc.mkUpdate(metricTotalNodes, float64(len(cc.Nodes)), "")
		}
		cc.valInfo.Window = params.Params.SignedBlocksWindow
		// x/slashing jails once the missed counter passes window - floor(min_signed_per_window * window)
		minSigned := params.Params.MinSignedPerWindow.MustFloat64()
		cc.valInfo.JailAfter = cc.valInfo.Window - int64(minSigned*float64(cc.valInfo.Window))
	}
	return
}

// getVal returns the public key, moniker, status flags and delegated stake of a validator.
func getVal(ctx context.Context, client *rpchttp.HTTP, valoper string) (pub []byte, moniker string, jailed, bonded bool, tokens float64, err error) {
	if strings.Contains(valoper, "valcons") {
		_, bz, err := bech32.DecodeAndConvert(valoper)
		if err != nil {
			return nil, "", false, false, 0, errors.New("could not decode and convert your address" + valoper)
		}
		return bz, valoper, false, true, 0, nil
	}

	val := &staking.QueryValidatorResponse{}
	err = abciQuery(ctx, client, "/cosmos.staking.v1beta1.Query/Validator", &staking.QueryValidatorRequest{ValidatorAddr: valoper}, val)
	if err != nil {
		return nil, "", false, false, 0, fmt.Errorf("could not find validator %s: %w", valoper, err)
	}
	if val.Validator.ConsensusPubkey == nil {
		return nil, "", false, false, 0, errors.New("got invalid consensus pubkey for " + valoper)
	}

	pubBytes := make([]byte, 0)
	switch val.Validator.ConsensusPubkey.TypeUrl {
	case "/cosmos.crypto.ed25519.PubKey":
		pk := ed25519.PubKey{}
		err = pk.Unmarshal(val.Validator.ConsensusPubkey.Value)
		if err != nil {
			return
		}
		pubBytes = pk.Address().Bytes()
	case "/cosmos.crypto.secp256k1.PubKey":
		pk := secp256k1.PubKey{}
		err = pk.Unmarshal(val.Validator.ConsensusPubkey.Value)
		if err != nil {
			return
		}
		pubBytes = pk.Address().Bytes()
	}
	if len(pubBytes) == 0 {
		return nil, "", false, false, 0, errors.New("could not get pubkey for" + valoper)
	}

	return pubBytes, val.Validator.GetMoniker(), val.Validator.Jailed, val.Validator.Status == 3, toFloat(val.Validator.Tokens), nil
}
