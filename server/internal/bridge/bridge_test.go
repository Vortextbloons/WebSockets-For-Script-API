package bridge

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSharedProtocol(t *testing.T) {
	raw, err := os.ReadFile("../../../test/protocol.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct{ Valid, Invalid []json.RawMessage }
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, raw := range fixtures.Valid {
		frame, err := Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := Encode(frame)
		if err != nil {
			t.Fatal(err)
		}
		var before, after any
		_ = json.Unmarshal(raw, &before)
		_ = json.Unmarshal(encoded, &after)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("changed envelope: %s", encoded)
		}
	}
	for _, raw := range fixtures.Invalid {
		if _, err := Decode(raw); err == nil {
			t.Fatalf("accepted invalid frame: %s", raw)
		}
	}
	if _, err := Decode([]byte("{")); err == nil {
		t.Fatal("accepted invalid JSON")
	}
	if _, err := Encode(Frame{V: 1, Kind: "event", Name: "large", Data: json.RawMessage(`"` + strings.Repeat("😀", 600) + `"`)}); Failure(err).Code != "PAYLOAD_TOO_LARGE" {
		t.Fatal(err)
	}
}

func receive(t *testing.T, b *Bridge, frame Frame) {
	t.Helper()
	raw, err := Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Receive(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
}

func waitFrame(t *testing.T, frames <-chan Frame) Frame {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(time.Second):
		t.Fatal("frame did not arrive")
		return Frame{}
	}
}

func TestRequestsCorrelateRepliesAndDisconnect(t *testing.T) {
	frames := make(chan Frame, 16)
	b := New(func(_ context.Context, frame Frame) error { frames <- frame; return nil }, nil)
	results := make(chan string, 2)
	for _, text := range []string{"first", "second"} {
		go func(text string) {
			raw, err := b.Request(context.Background(), "echo", text)
			if err != nil {
				results <- err.Error()
				return
			}
			results <- string(raw)
		}(text)
	}
	first, second := waitFrame(t, frames), waitFrame(t, frames)
	if first.ID == second.ID {
		t.Fatal("duplicate IDs")
	}
	receive(t, b, Frame{V: 1, Kind: "response", ID: second.ID, Data: second.Data})
	receive(t, b, Frame{V: 1, Kind: "response", ID: first.ID, Data: first.Data})
	got := map[string]bool{<-results: true, <-results: true}
	if !got[`"first"`] || !got[`"second"`] {
		t.Fatal(got)
	}
	done := make(chan error, 1)
	go func() { _, err := b.Request(context.Background(), "never", nil); done <- err }()
	waitFrame(t, frames)
	b.Close()
	if err := <-done; Failure(err).Code != "DISCONNECTED" {
		t.Fatal(err)
	}
}

func TestTimeoutLateResponseAndWriteFailure(t *testing.T) {
	frames := make(chan Frame, 16)
	b := New(func(_ context.Context, frame Frame) error { frames <- frame; return nil }, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if _, err := b.Request(ctx, "never", nil); Failure(err).Code != "TIMEOUT" {
		t.Fatal(err)
	}
	frame := waitFrame(t, frames)
	receive(t, b, Frame{V: 1, Kind: "response", ID: frame.ID, Data: json.RawMessage("null")})
	if len(b.pending) != 0 {
		t.Fatal("request leaked")
	}
	failing := New(func(context.Context, Frame) error { return &Error{Code: "DISCONNECTED", Message: "Write failed"} }, nil)
	if _, err := failing.Request(context.Background(), "echo", nil); Failure(err).Code != "DISCONNECTED" {
		t.Fatal(err)
	}
	if len(failing.pending) != 0 {
		t.Fatal("failed write leaked")
	}
}

func TestHandlersAndEvents(t *testing.T) {
	frames := make(chan Frame, 16)
	b := New(func(_ context.Context, frame Frame) error { frames <- frame; return nil }, nil)
	b.Handle("large", func(context.Context, json.RawMessage) (any, error) { return strings.Repeat("x", 3000), nil })
	b.Handle("invalid", func(context.Context, json.RawMessage) (any, error) {
		return nil, &Error{Code: "INVALID_DATA", Message: "Expected text"}
	})
	b.Handle("panic", func(context.Context, json.RawMessage) (any, error) { panic("broken") })
	for name, code := range map[string]string{"large": "PAYLOAD_TOO_LARGE", "invalid": "INVALID_DATA", "panic": "HANDLER_ERROR", "missing": "UNKNOWN_REQUEST"} {
		receive(t, b, Frame{V: 1, Kind: "request", ID: name, Name: name, Data: json.RawMessage("null")})
		if result := waitFrame(t, frames); result.Error == nil || result.Error.Code != code {
			t.Fatalf("unexpected response: %+v", result)
		}
	}
	count := 0
	stop := b.On("changed", func(json.RawMessage) { count++ })
	receive(t, b, Frame{V: 1, Kind: "event", Name: "changed", Data: json.RawMessage("null")})
	stop()
	receive(t, b, Frame{V: 1, Kind: "event", Name: "changed", Data: json.RawMessage("null")})
	if count != 1 || len(frames) != 0 {
		t.Fatal("incorrect event routing")
	}
}

func TestHandlerAndPendingLimits(t *testing.T) {
	frames := make(chan Frame, 64)
	b := New(func(_ context.Context, frame Frame) error { frames <- frame; return nil }, nil)
	started, release := make(chan struct{}, Limit), make(chan struct{})
	b.Handle("wait", func(context.Context, json.RawMessage) (any, error) { started <- struct{}{}; <-release; return nil, nil })
	for i := 0; i < Limit; i++ {
		receive(t, b, Frame{V: 1, Kind: "request", ID: string(rune('a' + i)), Name: "wait", Data: json.RawMessage("null")})
		<-started
	}
	receive(t, b, Frame{V: 1, Kind: "request", ID: "overflow", Name: "wait", Data: json.RawMessage("null")})
	if result := waitFrame(t, frames); result.Error.Code != "BUSY" {
		t.Fatal(result)
	}
	close(release)
	for i := 0; i < Limit; i++ {
		waitFrame(t, frames)
	}
	done := make(chan error, Limit)
	for i := 0; i < Limit; i++ {
		go func() { _, err := b.Request(context.Background(), "pending", nil); done <- err }()
		waitFrame(t, frames)
	}
	if _, err := b.Request(context.Background(), "overflow", nil); Failure(err).Code != "BUSY" {
		t.Fatal(err)
	}
	b.Close()
	for i := 0; i < Limit; i++ {
		if err := <-done; Failure(err).Code != "DISCONNECTED" {
			t.Fatal(err)
		}
	}
}
