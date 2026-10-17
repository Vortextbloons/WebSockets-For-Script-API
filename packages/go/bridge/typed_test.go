package bridge

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestErrorCodesMatchSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../testdata/protocol.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ErrorCodes []string `json:"errorCodes"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	want := []string{
		CodeInvalidFrame, CodeInvalidData, CodePayloadTooLarge, CodeUnknownRequest, CodeHandlerError,
		CodeListenerError, CodeTimeout, CodeBusy, CodeDisconnected, CodeNotReady, CodeCommandRejected,
		CodeIncompatible, CodeInternal,
	}
	got := append([]string(nil), fixture.ErrorCodes...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("error codes drifted from fixture:\nfixture: %v\ngo:      %v", got, want)
	}
}

func TestPingAnswersHeartbeats(t *testing.T) {
	frames := make(chan Frame, 4)
	b := New(func(_ context.Context, frame Frame) error { frames <- frame; return nil }, Options{})
	receive(t, b, Frame{V: 1, Kind: "request", ID: "ping-1", Name: "bridge.ping", Data: json.RawMessage("null")})
	result := waitFrame(t, frames)
	if result.Error != nil || string(result.Data) != "null" {
		t.Fatalf("unexpected ping reply: %+v", result)
	}
}

func TestListenerPanicIsolated(t *testing.T) {
	var reported []error
	b := New(func(context.Context, Frame) error { return nil }, Options{OnError: func(err error) { reported = append(reported, err) }})
	calls := 0
	b.On("changed", func(json.RawMessage) { panic("listener broke") })
	b.On("changed", func(json.RawMessage) { calls++ })
	if err := b.Receive(context.Background(), []byte(`{"v":1,"kind":"event","name":"changed","data":null}`)); err != nil {
		t.Fatalf("Receive returned %v after a listener panic", err)
	}
	if calls != 1 {
		t.Fatalf("healthy listener ran %d times", calls)
	}
	if len(reported) != 1 || Failure(reported[0]).Code != CodeListenerError || !strings.Contains(reported[0].Error(), "listener broke") {
		t.Fatalf("panic not reported: %v", reported)
	}
	receive(t, b, Frame{V: 1, Kind: "event", Name: "changed", Data: json.RawMessage("null")})
	if calls != 2 {
		t.Fatal("bridge stopped delivering events after a panic")
	}
}

func TestPanickingReporterDoesNotCrash(t *testing.T) {
	b := New(func(context.Context, Frame) error { return nil }, Options{OnError: func(error) { panic("reporter broke") }})
	b.On("changed", func(json.RawMessage) { panic("listener broke") })
	if err := b.Receive(context.Background(), []byte(`{"v":1,"kind":"event","name":"changed","data":null}`)); err != nil {
		t.Fatal(err)
	}
}

type echoRequest struct {
	Text *string `json:"text"`
}

func (r echoRequest) Validate() error {
	if r.Text == nil {
		return &Error{Code: CodeInvalidData, Message: "Expected {text: string}"}
	}
	return nil
}

type echoReply struct {
	Text string `json:"text"`
}

func TestTypedHandlersValidateInput(t *testing.T) {
	frames := make(chan Frame, 8)
	b := New(func(_ context.Context, frame Frame) error { frames <- frame; return nil }, Options{})
	HandleTyped(b, "app.echo", func(_ context.Context, req echoRequest) (echoReply, error) {
		return echoReply{Text: *req.Text}, nil
	})
	receive(t, b, Frame{V: 1, Kind: "request", ID: "good", Name: "app.echo", Data: json.RawMessage(`{"text":"hi","extra":1}`)})
	if result := waitFrame(t, frames); result.Error != nil || string(result.Data) != `{"text":"hi"}` {
		t.Fatalf("valid input rejected: %+v", result)
	}
	for _, data := range []string{`null`, `{}`, `{"text":5}`} {
		receive(t, b, Frame{V: 1, Kind: "request", ID: "bad", Name: "app.echo", Data: json.RawMessage(data)})
		if result := waitFrame(t, frames); result.Error == nil || result.Error.Code != CodeInvalidData {
			t.Fatalf("%s: want INVALID_DATA, got %+v", data, result)
		}
	}
}

func TestHandleTypedKeepsHandlerErrorCodes(t *testing.T) {
	frames := make(chan Frame, 2)
	b := New(func(_ context.Context, frame Frame) error { frames <- frame; return nil }, Options{})
	HandleTyped(b, "fail", func(context.Context, echoRequest) (echoReply, error) {
		return echoReply{}, &Error{Code: "NOT_FOUND", Message: "Missing item"}
	})
	receive(t, b, Frame{V: 1, Kind: "request", ID: "f", Name: "fail", Data: json.RawMessage(`{"text":"x"}`)})
	if result := waitFrame(t, frames); result.Error == nil || result.Error.Code != "NOT_FOUND" {
		t.Fatalf("custom code lost: %+v", result)
	}
}

func TestOnTypedSkipsInvalidEvents(t *testing.T) {
	var reported []error
	b := New(func(context.Context, Frame) error { return nil }, Options{OnError: func(err error) { reported = append(reported, err) }})
	var got []string
	OnTyped(b, "player.spawned", func(req echoRequest) { got = append(got, *req.Text) })
	receive(t, b, Frame{V: 1, Kind: "event", Name: "player.spawned", Data: json.RawMessage(`{"text":"Steve"}`)})
	receive(t, b, Frame{V: 1, Kind: "event", Name: "player.spawned", Data: json.RawMessage(`{"text":7}`)})
	if !reflect.DeepEqual(got, []string{"Steve"}) {
		t.Fatalf("listener calls: %v", got)
	}
	if len(reported) != 1 || Failure(reported[0]).Code != CodeInvalidData {
		t.Fatalf("invalid event not reported: %v", reported)
	}
}

func TestRequestTypedDecodesReplies(t *testing.T) {
	frames := make(chan Frame, 2)
	b := New(func(_ context.Context, frame Frame) error { frames <- frame; return nil }, Options{})
	done := make(chan error, 1)
	go func() {
		reply, err := RequestTyped[echoReply](context.Background(), b, "app.echo", map[string]string{"text": "hi"})
		if err == nil && reply.Text != "hi" {
			err = &Error{Code: CodeInvalidData, Message: "wrong reply"}
		}
		done <- err
	}()
	frame := waitFrame(t, frames)
	receive(t, b, Frame{V: 1, Kind: "response", ID: frame.ID, Data: json.RawMessage(`{"text":"hi"}`)})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
