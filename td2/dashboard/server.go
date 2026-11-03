package dash

import (
	"embed"
	"encoding/json"
	"github.com/gorilla/websocket"
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

// hub fans messages out to the connected dashboards. Each client has its own buffered channel, so a slow or dead
// client loses messages rather than holding everyone else up; the next status message carries the full state.
type hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func (h *hub) add() chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs == nil {
		h.subs = make(map[chan []byte]struct{})
	}
	h.subs[ch] = struct{}{}
	return ch
}

func (h *hub) remove(ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, ch)
}

func (h *hub) send(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

func Serve(port string, updates chan *ChainStatus, logs chan LogMessage, hideLogs bool) {
	var err error
	rootDir, err = fs.Sub(Content, "static")
	if err != nil {
		log.Fatalln(err)
	}
	cast := &hub{}

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
					cast.send(statusCache)
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
				cast.send(j)
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
		sub := cast.add()
		defer cast.remove(sub)
		// the client sends nothing we use, but reading is what answers pings and notices a closed tab
		gone := make(chan struct{})
		go func() {
			defer close(gone)
			for {
				if _, _, e := c.ReadMessage(); e != nil {
					return
				}
			}
		}()
		write := func(msg []byte) error {
			_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			return c.WriteMessage(websocket.TextMessage, msg)
		}
		// a fresh connection gets the current state at once rather than waiting for the next change
		statusMux.Lock()
		snapshot := statusCache
		statusMux.Unlock()
		if len(snapshot) > 0 {
			if e := write(snapshot); e != nil {
				return
			}
		}
		for {
			select {
			case <-gone:
				return
			case message := <-sub:
				if e := write(message); e != nil {
					return
				}
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
