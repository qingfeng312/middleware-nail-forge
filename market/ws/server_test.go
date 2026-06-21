package ws

import (
	"encoding/json"
	"testing"
)

func TestHandleInboundMessageRejectsInvalidJSON(t *testing.T) {
	client := newTestClient()

	client.handleInboundMessage([]byte(`{"type":"subscribe"`))

	errResp := readSocketError(t, client)
	if errResp.Code != "invalid_json" {
		t.Fatalf("expected invalid_json, got %q", errResp.Code)
	}
}

func TestHandleInboundMessageRejectsUnknownType(t *testing.T) {
	client := newTestClient()

	client.handleInboundMessage([]byte(`{"type":"publish","channel":"market.ticker","symbol":"BTC-USD"}`))

	errResp := readSocketError(t, client)
	if errResp.Code != "unknown_message_type" {
		t.Fatalf("expected unknown_message_type, got %q", errResp.Code)
	}
	if errResp.Details["type"] != "publish" {
		t.Fatalf("expected rejected type in details, got %#v", errResp.Details)
	}
}

func TestHandleInboundMessageRejectsBadSymbol(t *testing.T) {
	client := newTestClient()

	client.handleInboundMessage([]byte(`{"type":"subscribe","channel":"market.ticker","symbol":"BTCUSD"}`))

	errResp := readSocketError(t, client)
	if errResp.Code != "invalid_symbol" {
		t.Fatalf("expected invalid_symbol, got %q", errResp.Code)
	}
	if errResp.Details["format"] != "BASE-QUOTE" {
		t.Fatalf("expected BASE-QUOTE format hint, got %#v", errResp.Details)
	}
}

func TestHandleInboundMessageDeduplicatesSubscriptions(t *testing.T) {
	client := newTestClient()
	msg := []byte(`{"type":"subscribe","channel":"market.ticker","symbol":"btc/usd"}`)

	client.handleInboundMessage(msg)
	ack := readSubscriptionAck(t, client)
	if ack.Type != "subscribed" || ack.Symbol != "BTC-USD" || ack.Channel != "market.ticker" {
		t.Fatalf("unexpected subscription ack: %#v", ack)
	}
	if len(client.subs) != 1 {
		t.Fatalf("expected one subscription, got %d", len(client.subs))
	}

	client.handleInboundMessage(msg)
	errResp := readSocketError(t, client)
	if errResp.Code != "duplicate_subscription" {
		t.Fatalf("expected duplicate_subscription, got %q", errResp.Code)
	}
	if len(client.subs) != 1 {
		t.Fatalf("duplicate should not add a second subscription, got %d", len(client.subs))
	}
}

func TestNormalizeMarketSymbol(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "hyphen format", in: "ETH-USD", want: "ETH-USD"},
		{name: "slash is normalized", in: " sol/usd ", want: "SOL-USD"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeMarketSymbol(tc.in)
			if err != nil {
				t.Fatalf("normalizeMarketSymbol(%q) returned error: %v", tc.in, err)
			}
			if string(got) != tc.want {
				t.Fatalf("normalizeMarketSymbol(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func newTestClient() *Client {
	return &Client{
		send: make(chan []byte, 4),
		subs: make(map[subscriptionKey]struct{}),
	}
}

func readSocketError(t *testing.T, client *Client) socketError {
	t.Helper()

	var errResp socketError
	if err := json.Unmarshal(<-client.send, &errResp); err != nil {
		t.Fatalf("failed to decode socket error: %v", err)
	}
	if errResp.Type != "error" {
		t.Fatalf("expected error response, got %#v", errResp)
	}
	return errResp
}

func readSubscriptionAck(t *testing.T, client *Client) subscriptionAck {
	t.Helper()

	var ack subscriptionAck
	if err := json.Unmarshal(<-client.send, &ack); err != nil {
		t.Fatalf("failed to decode subscription ack: %v", err)
	}
	return ack
}
