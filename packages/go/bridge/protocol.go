package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
)

const MaxBytes = 2048

// Standard error codes. The TypeScript package exports the same names and values.
const (
	CodeInvalidFrame    = "INVALID_FRAME"
	CodeInvalidData     = "INVALID_DATA"
	CodePayloadTooLarge = "PAYLOAD_TOO_LARGE"
	CodeUnknownRequest  = "UNKNOWN_REQUEST"
	CodeHandlerError    = "HANDLER_ERROR"
	CodeListenerError   = "LISTENER_ERROR"
	CodeTimeout         = "TIMEOUT"
	CodeBusy            = "BUSY"
	CodeDisconnected    = "DISCONNECTED"
	CodeNotReady        = "NOT_READY"
	CodeCommandRejected = "COMMAND_REJECTED"
	CodeIncompatible    = "INCOMPATIBLE"
	CodeInternal        = "INTERNAL_ERROR"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Errorf builds a named error with a formatted message.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func Failure(err error) *Error {
	var result *Error
	if errors.As(err, &result) {
		return result
	}
	return &Error{Code: CodeHandlerError, Message: err.Error()}
}

type Frame struct {
	V     int             `json:"v"`
	Kind  string          `json:"kind"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *Error          `json:"error,omitempty"`
}

func Encode(frame Frame) ([]byte, error) {
	raw, err := json.Marshal(frame)
	if err != nil {
		return nil, &Error{Code: CodeInvalidData, Message: "Data must be JSON"}
	}
	if len(raw) > MaxBytes {
		return nil, &Error{Code: CodePayloadTooLarge, Message: "Message exceeds 2048 UTF-8 bytes"}
	}
	if _, err := Decode(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func Decode(raw []byte) (Frame, error) {
	var frame Frame
	if len(raw) > MaxBytes {
		return frame, &Error{Code: CodePayloadTooLarge, Message: "Message exceeds 2048 UTF-8 bytes"}
	}
	if json.Unmarshal(raw, &frame) != nil || frame.V != 1 {
		return frame, &Error{Code: CodeInvalidFrame, Message: "Invalid bridge message or version"}
	}
	valid := false
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	switch frame.Kind {
	case "request":
		valid = frame.ID != "" && len(frame.ID) <= 128 && frame.Name != "" && frame.Data != nil && fields["error"] == nil
	case "event":
		valid = fields["id"] == nil && frame.Name != "" && frame.Data != nil && fields["error"] == nil
	case "response":
		valid = frame.ID != "" && len(frame.ID) <= 128 && fields["name"] == nil && ((frame.Data != nil) != (fields["error"] != nil))
		valid = valid && (fields["error"] == nil || frame.Error != nil)
		if frame.Error != nil {
			valid = valid && frame.Error.Code != "" && frame.Error.Message != ""
		}
	}
	if !valid {
		return frame, &Error{Code: CodeInvalidFrame, Message: "Invalid bridge envelope"}
	}
	return frame, nil
}
