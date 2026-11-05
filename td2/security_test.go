package tenderduty

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"

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
