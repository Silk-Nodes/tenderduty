package tenderduty

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
)

// cosmosPaths maps chain ids to their path on cosmos.directory, used to find a public RPC node. It is filled from
// the registry at startup and refreshed every 12 hours.
var cosmosPaths = map[string]string{}
var pathMux sync.Mutex

const registryJson = "https://chains.cosmos.directory/"
const publicRpcUrl = "https://rpc.cosmos.directory:443/"

// a trimmed down version only holding the info we need to create a lookup map
type registryResults struct {
	Chains []struct {
		Path    string `json:"path"`
		ChainId string `json:"chain_id"`
	} `json:"chains"`
}

// refreshRegistry updates the path map for public RPC endpoints for @eco_stake's public RPC proxy
func refreshRegistry() error {
	res, err := http.Get(registryJson)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	chains := &registryResults{}
	err = json.Unmarshal(body, chains)
	if err != nil {
		return err
	}
	if len(chains.Chains) == 0 {
		return errors.New("response had no chains")
	}
	pathMux.Lock()
	defer pathMux.Unlock()
	for _, c := range chains.Chains {
		cosmosPaths[c.ChainId] = c.Path
	}
	return nil
}

func getRegistryUrl(chainid string) (url string, ok bool) {
	pathMux.Lock()
	defer pathMux.Unlock()
	return publicRpcUrl + cosmosPaths[chainid], cosmosPaths[chainid] != ""
}
