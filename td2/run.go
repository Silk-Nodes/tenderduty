package tenderduty

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	dash "github.com/Silk-Nodes/tenderduty/v2/td2/dashboard"
)

var td = &Config{}

// guard runs fn again after a panic, so one bad answer from a node restarts one goroutine instead of ending
// the monitor for every chain. A normal return is final.
func guard(name string, fn func()) {
	for {
		panicked := false
		func() {
			defer func() {
				if r := recover(); r != nil {
					panicked = true
					l(fmt.Sprintf("🚑 %s recovered from panic: %v, restarting in 5s", name, r))
				}
			}()
			fn()
		}()
		if !panicked {
			return
		}
		time.Sleep(5 * time.Second)
	}
}

func Run(configFile, stateFile, chainConfigDirectory string, password *string) error {
	var err error
	td, err = loadConfig(configFile, stateFile, chainConfigDirectory, password)
	if err != nil {
		return err
	}
	fatal, problems := validateConfig(td)
	for _, p := range problems {
		fmt.Println(p)
	}
	if fatal {
		log.Fatal("tenderduty the configuration is invalid, refusing to start")
	}
	log.Println("tenderduty config is valid, starting tenderduty with", len(td.Chains), "chains")

	defer td.cancel()

	go func() {
		for {
			select {
			case alert := <-td.alertChan:
				// each channel in its own goroutine, so a slow webhook never holds the alert back from the others
				for name, send := range map[string]func(*alertMsg) error{
					"pagerduty": notifyPagerduty, "discord": notifyDiscord, "telegram": notifyTg, "slack": notifySlack,
				} {
					go func(name string, send func(*alertMsg) error, msg *alertMsg) {
						if e := send(msg); e != nil {
							l(msg.chain, "error sending alert to "+name, e.Error())
						}
					}(name, send, alert)
				}
			case <-td.ctx.Done():
				return
			}
		}
	}()

	if td.EnableDash {
		go dash.Serve(td.listenAddr(), td.updateChan, td.logChan, td.HideLogs)
		l("starting dashboard on", td.listenAddr())
	} else {
		go func() {
			for {
				<-td.updateChan
			}
		}()
	}
	if td.Prom {
		go prometheusExporter(td.ctx, td.statsChan)
	} else {
		go func() {
			for {
				<-td.statsChan
			}
		}()
	}

	// tenderduty health checks:
	if td.Healthcheck.Enabled {
		td.pingHealthcheck()
	}

	for k := range td.Chains {
		cc := td.Chains[k]

		go func(cc *ChainConfig) {
			// alert worker
			go guard(cc.name+" alerts", cc.watch)

			// node health checks:
			go guard(cc.name+" health", func() { cc.monitorHealth(td.ctx) })

			// websocket subscription and occasional validator info refreshes
			guard(cc.name+" monitor", func() { cc.monitor() })
		}(cc)
	}

	// attempt to save state on exit, only a best-effort ...
	saved := make(chan interface{})
	go saveOnExit(stateFile, saved)

	<-td.ctx.Done()
	<-saved

	return err
}

func saveOnExit(stateFile string, saved chan interface{}) {
	quitting := make(chan os.Signal, 1)
	signal.Notify(quitting, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	saveState := func() {
		defer close(saved)
		log.Println("saving state...")
		//#nosec -- variable specified on command line
		f, e := os.OpenFile(stateFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if e != nil {
			log.Println(e)
			return
		}
		td.chainsMux.Lock()
		defer td.chainsMux.Unlock()
		blocks := make(map[string][]int)
		// only need to save counts if the dashboard exists
		if td.EnableDash {
			for k, v := range td.Chains {
				blocks[k] = v.blocksResults
			}
		}
		nodesDown := make(map[string]map[string]time.Time)
		for k, v := range td.Chains {
			for _, node := range v.Nodes {
				if node.down {
					if nodesDown[k] == nil {
						nodesDown[k] = make(map[string]time.Time)
					}
					nodesDown[k][node.Url] = node.downSince
				}
			}
		}
		b, e := json.Marshal(&savedState{
			Alarms:        alarms,
			Blocks:        blocks,
			NodesDown:     nodesDown,
			ConfigVersion: td.ConfigVersion,
		})
		if e != nil {
			log.Println(e)
			return
		}
		_, _ = f.Write(b)
		_ = f.Close()
		log.Println("tenderduty exiting.")
	}
	for {
		select {
		case <-td.ctx.Done():
			saveState()
			return
		case <-quitting:
			saveState()
			td.cancel()
			return
		}
	}
}

// monitor keeps one chain connected: pick a node, refresh the validator, run the websocket, repeat.
func (cc *ChainConfig) monitor() {
	for {
		e := cc.newRpc()
		if e != nil {
			l(cc.ChainId, e)
			time.Sleep(5 * time.Second)
			continue
		}
		if !cc.rpcOnly() {
			if e = cc.GetValInfo(true); e != nil {
				l("🛑", cc.ChainId, e)
			}
		}
		started := time.Now()
		cc.WsRun()
		// a websocket that dies within minutes of connecting points at a node that answers status
		// calls but does not deliver blocks, so leave it alone for a while and try the others first.
		if node := cc.currentNode(); node != nil && time.Since(started) < 2*time.Minute {
			node.wsFailedAt = time.Now()
			l(cc.ChainId, "🌀 websocket to", node.Url, "failed shortly after connecting, preferring other nodes")
		} else {
			l(cc.ChainId, "🌀 websocket exited! Restarting monitoring")
		}
		time.Sleep(5 * time.Second)
	}
}
