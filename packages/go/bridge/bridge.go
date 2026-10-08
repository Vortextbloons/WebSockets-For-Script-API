package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const Timeout = 10 * time.Second
const Limit = 16

type Handler func(context.Context, json.RawMessage) (any, error)
type Sender func(context.Context, Frame) error
type Observer func(string, Frame)

type Options struct {
	Observe Observer
	Timeout time.Duration
}

type Bridge struct {
	send     Sender
	observe  Observer
	timeout  time.Duration
	prefix   string
	sequence atomic.Uint64
	mu       sync.Mutex
	closed   bool
	handlers map[string]Handler
	events   map[string]map[uint64]func(json.RawMessage)
	pending  map[string]chan Frame
	workers  chan struct{}
}

func New(send Sender, options Options) *Bridge {
	if options.Timeout == 0 {
		options.Timeout = Timeout
	}
	if options.Timeout < 0 {
		panic("request timeout must be positive")
	}
	b := &Bridge{
		send: send, observe: options.Observe, timeout: options.Timeout, prefix: fmt.Sprintf("peer-%d", time.Now().UnixNano()),
		handlers: make(map[string]Handler), events: make(map[string]map[uint64]func(json.RawMessage)),
		pending: make(map[string]chan Frame), workers: make(chan struct{}, Limit),
	}
	b.Handle("bridge.hello", func(context.Context, json.RawMessage) (any, error) {
		return map[string]int{"protocol": 1}, nil
	})
	return b
}

func (b *Bridge) Handle(name string, handler Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || name == "" || handler == nil || b.handlers[name] != nil {
		panic("invalid or duplicate request handler: " + name)
	}
	b.handlers[name] = handler
}

func (b *Bridge) On(name string, listener func(json.RawMessage)) func() {
	id := b.sequence.Add(1)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		panic("bridge closed")
	}
	if b.events[name] == nil {
		b.events[name] = make(map[uint64]func(json.RawMessage))
	}
	b.events[name][id] = listener
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.events[name], id)
		b.mu.Unlock()
	}
}

func (b *Bridge) Request(ctx context.Context, name string, data any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	raw, err := json.Marshal(data)
	if err != nil || name == "" {
		return nil, &Error{Code: "INVALID_DATA", Message: "Expected a request name and JSON data"}
	}
	id := fmt.Sprintf("%s-%d", b.prefix, b.sequence.Add(1))
	reply := make(chan Frame, 1)
	b.mu.Lock()
	if b.closed || len(b.pending) >= Limit {
		closed := b.closed
		b.mu.Unlock()
		if closed {
			return nil, &Error{Code: "DISCONNECTED", Message: "Bridge closed"}
		}
		return nil, &Error{Code: "BUSY", Message: "Too many pending requests"}
	}
	b.pending[id] = reply
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()
	if err := b.Send(ctx, Frame{V: 1, Kind: "request", ID: id, Name: name, Data: raw}); err != nil {
		return nil, err
	}
	select {
	case result := <-reply:
		if result.Error != nil {
			return nil, result.Error
		}
		return result.Data, nil
	case <-ctx.Done():
		return nil, &Error{Code: "TIMEOUT", Message: "Request ended before a result arrived"}
	}
}

func (b *Bridge) Emit(ctx context.Context, name string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil || name == "" {
		return &Error{Code: "INVALID_DATA", Message: "Expected an event name and JSON data"}
	}
	return b.Send(ctx, Frame{V: 1, Kind: "event", Name: name, Data: raw})
}

func (b *Bridge) Send(ctx context.Context, frame Frame) error {
	if _, err := Encode(frame); err != nil {
		return err
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed || b.send == nil {
		return &Error{Code: "DISCONNECTED", Message: "Bridge unavailable"}
	}
	if err := b.send(ctx, frame); err != nil {
		return err
	}
	if b.observe != nil {
		b.observe("out", frame)
	}
	return nil
}

func (b *Bridge) Call(ctx context.Context, name string, data json.RawMessage) (result any, err error) {
	select {
	case b.workers <- struct{}{}:
		defer func() { <-b.workers }()
	default:
		return nil, &Error{Code: "BUSY", Message: "Too many active handlers"}
	}
	return b.call(ctx, name, data)
}

func (b *Bridge) call(ctx context.Context, name string, data json.RawMessage) (result any, err error) {
	if ctx.Err() != nil {
		return nil, &Error{Code: "TIMEOUT", Message: "Request deadline elapsed"}
	}
	b.mu.Lock()
	handler, closed := b.handlers[name], b.closed
	b.mu.Unlock()
	if closed {
		return nil, &Error{Code: "DISCONNECTED", Message: "Bridge closed"}
	}
	if handler == nil {
		return nil, &Error{Code: "UNKNOWN_REQUEST", Message: "Unknown request: " + name}
	}
	defer func() {
		if recover() != nil {
			result, err = nil, &Error{Code: "HANDLER_ERROR", Message: "Handler panicked"}
		}
	}()
	return handler(ctx, data)
}

func (b *Bridge) Receive(ctx context.Context, raw []byte) error {
	frame, err := Decode(raw)
	if err != nil {
		return err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	if b.observe != nil {
		b.observe("in", frame)
	}
	b.mu.Lock()
	switch frame.Kind {
	case "response":
		if reply := b.pending[frame.ID]; reply != nil {
			delete(b.pending, frame.ID)
			reply <- frame
		}
	case "event":
		listeners := make([]func(json.RawMessage), 0, len(b.events[frame.Name]))
		for _, listener := range b.events[frame.Name] {
			listeners = append(listeners, listener)
		}
		b.mu.Unlock()
		for _, listener := range listeners {
			listener(frame.Data)
		}
		b.mu.Lock()
	case "request":
		select {
		case b.workers <- struct{}{}:
		default:
			b.mu.Unlock()
			return b.Send(ctx, Frame{V: 1, Kind: "response", ID: frame.ID, Error: &Error{Code: "BUSY", Message: "Too many active handlers"}})
		}
		go func() {
			defer func() { <-b.workers }()
			ctx, cancel := context.WithTimeout(ctx, b.timeout)
			defer cancel()
			result, err := b.call(ctx, frame.Name, frame.Data)
			response := Frame{V: 1, Kind: "response", ID: frame.ID}
			if err == nil {
				response.Data, err = json.Marshal(result)
			}
			if err != nil {
				response.Data, response.Error = nil, Failure(err)
			}
			if _, err := Encode(response); err != nil {
				response.Data, response.Error = nil, Failure(err)
			}
			_ = b.Send(ctx, response)
		}()
	}
	b.mu.Unlock()
	return nil
}

func (b *Bridge) Reject(id string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if reply := b.pending[id]; reply != nil {
		delete(b.pending, id)
		reply <- Frame{Error: Failure(err)}
	}
}

func (b *Bridge) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	clear(b.handlers)
	clear(b.events)
	for id, reply := range b.pending {
		reply <- Frame{Error: &Error{Code: "DISCONNECTED", Message: "Bridge closed"}}
		delete(b.pending, id)
	}
}
