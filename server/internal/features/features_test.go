package features

import (
	"context"
	"encoding/json"
	"testing"

	"scriptbridge/bridge"
)

func TestEchoContract(t *testing.T) {
	b := bridge.New(nil, bridge.Options{})
	defer b.Close()
	Register(b)
	for _, input := range []string{`{}`, `{"text":null}`, `{"text":123}`} {
		_, err := b.Call(context.Background(), AppEcho, json.RawMessage(input))
		if err == nil || bridge.Failure(err).Code != bridge.CodeInvalidData {
			t.Fatalf("expected INVALID_DATA for %s, got %v", input, err)
		}
	}
	reply, err := b.Call(context.Background(), AppEcho, json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(reply)
	if err != nil || string(raw) != `{"text":"hello"}` {
		t.Fatalf("unexpected reply %s: %v", raw, err)
	}
}
