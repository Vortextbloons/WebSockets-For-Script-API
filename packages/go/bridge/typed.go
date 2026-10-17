package bridge

import (
	"context"
	"encoding/json"
	"errors"
)

// Validator is an optional schema hook. Implement Validate on a payload type to reject
// JSON that decodes correctly but breaks your rules, such as a missing required field.
type Validator interface {
	Validate() error
}

// Parse decodes raw JSON into T and runs Validate when T implements Validator.
// Any failure is returned as an INVALID_DATA error that is safe to send to peers.
func Parse[T any](raw json.RawMessage) (T, error) {
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, &Error{Code: CodeInvalidData, Message: "Invalid data: " + err.Error()}
	}
	if validator, ok := any(&value).(Validator); ok {
		if err := validator.Validate(); err != nil {
			return value, invalid(err)
		}
	}
	return value, nil
}

func invalid(err error) error {
	var coded *Error
	if errors.As(err, &coded) {
		return coded
	}
	return &Error{Code: CodeInvalidData, Message: err.Error()}
}

// HandleTyped registers a request handler whose input is decoded and validated as Req.
// Handler errors follow the same rules as Handle: *Error values keep their code.
func HandleTyped[Req, Res any](b *Bridge, name string, handler func(context.Context, Req) (Res, error)) {
	b.Handle(name, func(ctx context.Context, raw json.RawMessage) (any, error) {
		req, err := Parse[Req](raw)
		if err != nil {
			return nil, err
		}
		result, err := handler(ctx, req)
		return result, err
	})
}

// RequestTyped sends a request and decodes the reply as Res.
// A malformed reply returns INVALID_DATA.
func RequestTyped[Res any](ctx context.Context, b *Bridge, name string, data any) (Res, error) {
	var zero Res
	raw, err := b.Request(ctx, name, data)
	if err != nil {
		return zero, err
	}
	return Parse[Res](raw)
}

// OnTyped subscribes to an event whose payload decodes as T. A payload that fails to decode
// is reported through OnError and skipped, so one bad event cannot stop the listener.
func OnTyped[T any](b *Bridge, name string, listener func(T)) func() {
	return b.On(name, func(raw json.RawMessage) {
		value, err := Parse[T](raw)
		if err != nil {
			b.report(&Error{Code: CodeInvalidData, Message: "Event " + name + ": " + err.Error()})
			return
		}
		listener(value)
	})
}
