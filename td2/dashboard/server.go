package dash

import (
	"embed"
	"encoding/json"
	"github.com/gorilla/websocket"
	"github.com/textileio/go-threads/broadcast"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	Content embed.FS
	rootDir fs.FS
	rex     = regexp.MustCompile(`\W(https?|tcp|wss?)://.+\w`)
)

const logLength = 256

func Serve(port string, updates chan *ChainStatus, logs chan LogMessage, hideLogs bool) {
	var err error
	rootDir, err = fs.Sub(Content, "static")
	if err != nil {
		log.Fatalln(err)
	}
	var cast broadcast.Broadcaster

	// cache the json .... don't serialize on-demand
	logCache, statusCache := []byte{'[', ']'}, []byte{'{', '}'}

	statusMux := sync.Mutex{}
	status := make(map[string]*ChainStatus)
	logSlice := make([]LogMessage, 0)

	type statusUpdate struct {
		MessageType string `json:"msgType"`
		Status      []*ChainStatus
	}

	go func() {
		tick := time.NewTicker(time.Second)
		update := false
		for {
			select {
			case <-tick.C:
				if update {
					_ = cast.Send(statusCache)
					update = false
				}

			case u := <-updates:
				// try to catch any accidental rpc endpoint leaks
				if hideLogs {
					u.LastError = rex.ReplaceAllString(u.LastError, "-redacted-")
					for i := range u.NodeStatus {
						u.NodeStatus[i].Message = rex.ReplaceAllString(u.NodeStatus[i].Message, "-redacted-")
					}
				}
				status[u.Name] = u
				result := make([]*ChainStatus, 0)
				for k := range status {
					result = append(result, status[k])
				}
				sort.Slice(result, func(i, j int) bool {
					return result[i].Name < result[j].Name
				})
				j, e := json.Marshal(statusUpdate{
					MessageType: "update",
					Status:      result,
				})
				if e != nil {
					continue
				}
				statusMux.Lock()
				statusCache = j
				statusMux.Unlock()
				update = true

			case l := <-logs:
				if hideLogs {
					continue
				}
				if len(logSlice) >= logLength {
					logSlice = append([]LogMessage{l}, logSlice[0:len(logSlice)-1]...)
				} else {
					logSlice = append([]LogMessage{l}, logSlice...)
				}
				j, e := json.Marshal(logSlice)
				if e != nil {
					continue
				}
				logCache = j
				j, e = json.Marshal(l)
				if e != nil {
					continue
				}
				_ = cast.Send(j)
			}
		}
	}()

	var upgrader = websocket.Upgrader{}
	upgrader.CheckOrigin = func(r *http.Request) bool { return true }
	upgrader.EnableCompression = true

	http.HandleFunc("/ws", func(writer http.ResponseWriter, request *http.Request) {
		c, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer c.Close()
		sub := cast.Listen()
		defer sub.Discard()
		// a fresh connection gets the current state at once rather than waiting for the next change
		statusMux.Lock()
		snapshot := statusCache
		statusMux.Unlock()
		if len(snapshot) > 0 {
			if e := c.WriteMessage(websocket.TextMessage, snapshot); e != nil {
				return
			}
		}
		for message := range sub.Channel() {
			e := c.WriteMessage(websocket.TextMessage, message.([]byte))
			if e != nil {
				return
			}
		}
	})

	http.HandleFunc("/logsenabled", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		j, _ := json.Marshal(map[string]bool{"enabled": !hideLogs})
		_, _ = writer.Write(j)
	})

	http.HandleFunc("/logs", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = writer.Write(logCache)
	})

	http.HandleFunc("/state", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		statusMux.Lock()
		defer statusMux.Unlock()
		_, _ = writer.Write(statusCache)
	})

	http.Handle("/", &CacheHandler{})
	server := &http.Server{
		Addr:              ":" + port,
		ReadHeaderTimeout: 3 * time.Second,
	}
	err = server.ListenAndServe()
	cast.Discard()
	log.Fatal("tenderduty dashboard server failed", err)
}

// CacheHandler serves the static files. Vendored css and js may be cached for an hour, the page and its own
// scripts are revalidated on every load so a new release shows up without clearing the browser cache.
type CacheHandler struct{}

func (ch CacheHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/css/") || strings.HasPrefix(request.URL.Path, "/js/") {
		writer.Header().Set("Cache-Control", "public, max-age=3600")
	} else {
		writer.Header().Set("Cache-Control", "no-cache")
	}
	writer.Header().Set("X-Powered-By", "https://github.com/Silk-Nodes/tenderduty")
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	http.FileServer(http.FS(rootDir)).ServeHTTP(writer, request)
}
