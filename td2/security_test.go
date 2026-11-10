package tenderduty

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
)

// Regression checks for the security audit fixes (run-1, 2026-11-04).

func TestDedupScopedByChain(t *testing.T) {
	const text = "node is no longer active on c-1: validator is jailed"
	a := &alertMsg{chain: "a", message: text, disc: true}
	b := &alertMsg{chain: "b", message: text, disc: true}
	if !shouldNotify(a, di) {
		t.Fatal("first alert on chain a should notify")
	}
	if !shouldNotify(b, di) {
		t.Error("the same text on chain b is a different alarm and must notify")
	}
	if shouldNotify(a, di) {
		t.Error("a repeat on chain a must stay suppressed")
	}
	for _, m := range []*alertMsg{a, b} {
		m.resolved = true
		shouldNotify(m, di)
	}
}

func TestChatMarkupEscaped(t *testing.T) {
	msg := &alertMsg{chain: "c", chainId: "c-1", message: "proposal #1 ([x](https://e) <!channel> @everyone) on c-1 is open for voting until 2026-01-01 00:00 UTC and v has not voted"}
	if d := buildDiscordMessage(msg).Embeds[0].Description; !strings.Contains(d, `\[x\](https://e)`) || strings.Contains(d, "@everyone") {
		t.Errorf("discord description = %q", d)
	}
	if s := buildSlackMessage(msg).Attachments[0].Text; !strings.Contains(s, "&lt;!channel&gt;") {
		t.Errorf("slack text = %q", s)
	}
}

func TestSendErrDropsSecrets(t *testing.T) {
	raw := &url.Error{Op: "Post", URL: "https://discord.com/api/webhooks/1/SECRET", Err: errors.New("connection refused")}
	if s := sendErr(raw).Error(); strings.Contains(s, "SECRET") || !strings.Contains(s, "connection refused") {
		t.Errorf("url.Error not stripped: %q", s)
	}
	if s := sendErr(errors.New("bot TOKEN/getMe failed"), "TOKEN").Error(); strings.Contains(s, "TOKEN") {
		t.Errorf("secret not blanked: %q", s)
	}
}

func TestDecryptRejectsMalformed(t *testing.T) {
	zeros := make([]byte, base64.StdEncoding.EncodedLen(120))
	base64.StdEncoding.Encode(zeros, make([]byte, 120))
	tail := append(make([]byte, 20), make([]byte, 80)...)
	for i := range tail[:20] {
		tail[i] = 1
	}
	tailB64 := make([]byte, base64.StdEncoding.EncodedLen(len(tail)))
	base64.StdEncoding.Encode(tailB64, tail)
	for name, body := range map[string][]byte{"empty": {}, "all zeros": zeros, "zero tail": tailB64} {
		if _, err := decrypt(body, "4G*vk90-long-enough-password"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDecryptRoundTripEveryPadding(t *testing.T) {
	const pass = "4G*vk90-long-enough-password"
	for n := 1; n <= 48; n++ {
		plain := []byte(strings.Repeat("x", n))
		enc, err := encrypt(plain, pass)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decrypt(enc, pass)
		if err != nil || string(got) != string(plain) {
			t.Fatalf("length %d: got %q err %v", n, got, err)
		}
	}
	// a MAC whose last byte is 0x00 used to be trimmed and fail; about one in 256 encryptions produces one
	for i := 0; i < 400; i++ {
		enc, _ := encrypt([]byte("config: x"), pass)
		dec := make([]byte, base64.StdEncoding.DecodedLen(len(enc)))
		n, _ := base64.StdEncoding.Decode(dec, enc)
		if dec[n-1] == 0 {
			if _, err := decrypt(enc, pass); err != nil {
				t.Fatalf("MAC ending in 0x00 rejected: %v", err)
			}
			return
		}
	}
	t.Log("no MAC ending in 0x00 in 400 tries, that case not exercised")
}

func TestConsAddressRejectsBadKeys(t *testing.T) {
	if _, err := consAddress(nil); err == nil {
		t.Error("nil pubkey must be an error, not a panic")
	}
	short, _ := (&ed25519.PubKey{Key: []byte{1, 2, 3, 4, 5}}).Marshal()
	if _, err := consAddress(&codectypes.Any{TypeUrl: "/cosmos.crypto.ed25519.PubKey", Value: short}); err == nil {
		t.Error("5 byte ed25519 key must be an error, not a panic")
	}
}

func TestCleanNodeString(t *testing.T) {
	if got := clean("bad\xff\x01name"+strings.Repeat("y", 100), 70); !strings.HasPrefix(got, "bad�name") || len([]rune(got)) != 70 {
		t.Errorf("clean = %q", got)
	}
}

func TestClearStaleKeepsLongLivedAlarms(t *testing.T) {
	old := time.Now().Add(-48 * time.Hour)
	sent := map[string]time.Time{
		"Zeta\x00proposal #70 (x) on zetachain_7000-1 is open for voting until 2026-11-07 16:12 UTC and v has not voted": old,
		"Babylon\x00upgrade v4.7 is scheduled at height 4980300 on bbn-1":                                                old,
		"Mantra\x00v has missed 10 blocks on mantra-1":                                                                   old,
		"Mantra\x00RPC node a is down":                                                                                   time.Now(),
	}
	clearStale(sent, "Discord", false, staleHours)
	if len(sent) != 3 {
		t.Fatalf("expected the proposal, the upgrade and the fresh node alarm to survive, got %v", sent)
	}
	for k := range sent {
		if strings.Contains(k, "has missed") {
			t.Fatalf("stale missed-blocks alarm was restored")
		}
	}
}

func TestDiscordTimeMarkup(t *testing.T) {
	msg := &alertMsg{chain: "c", chainId: "c-1", message: "proposal #1 (t) on c-1 is open for voting until 2026-11-12 20:27 UTC and v has not voted"}
	got := buildDiscordMessage(msg).Embeds[0].Description
	if !strings.Contains(got, "<t:1794515220:f>") || strings.Contains(got, "UTC") {
		t.Fatalf("description %q", got)
	}
	if discordTime("no time here") != "no time here" {
		t.Fatal("text without a time changed")
	}
	if s := buildSlackMessage(msg).Attachments[0].Text; !strings.Contains(s, "<!date^1794515220^{date_short_pretty} at {time}|2026-11-12 20:27 UTC>") {
		t.Fatalf("slack text %q", s)
	}
}

func TestTelegramText(t *testing.T) {
	msg := &alertMsg{chain: "c", chainId: "c-1", severity: "info", message: "proposal #1 (<b>x</b>) on c-1 is open for voting until 2026-11-12 20:27 UTC and v has not voted"}
	got := telegramText(msg)
	if !strings.HasPrefix(got, "<b>") || !strings.Contains(got, "&lt;b&gt;x&lt;/b&gt;") || !strings.Contains(got, "Chain: c-1  Severity: info") {
		t.Fatalf("telegram text %q", got)
	}
	msg.resolved = true
	if strings.Contains(telegramText(msg), "Severity") {
		t.Fatal("resolve carries a severity")
	}
}
