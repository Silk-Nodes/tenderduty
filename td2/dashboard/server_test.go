package dash

import (
	_ "expvar" // registers /debug/vars on the default mux, as the prometheus client does in the real binary
	"net/http"
	"testing"
	"time"
)

// The dashboard must serve only its own routes, to its own origin, and must not list directories.
func TestServeOnlyOwnRoutes(t *testing.T) {
	updates := make(chan *ChainStatus)
	logs := make(chan LogMessage)
	go Serve("127.0.0.1:18899", updates, logs, false)
	client := &http.Client{Timeout: 5 * time.Second}
	var resp *http.Response
	var err error
	for i := 0; i < 50; i++ {
		if resp, err = client.Get("http://127.0.0.1:18899/logsenabled"); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	for path, want := range map[string]int{"/debug/vars": 404, "/css/": 404, "/state": 200, "/logs": 200} {
		resp, err := client.Get("http://127.0.0.1:18899" + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s = %d, want %d", path, resp.StatusCode, want)
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s sends a CORS header", path)
		}
	}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:18899/ws", nil)
	req.Header.Set("Origin", "https://attacker.example")
	if !sameOrigin(&http.Request{Host: "127.0.0.1:18899", Header: http.Header{"Origin": {"http://127.0.0.1:18899"}}}) {
		t.Error("own origin refused")
	}
	if sameOrigin(req) {
		t.Error("foreign origin accepted on /ws")
	}
}
