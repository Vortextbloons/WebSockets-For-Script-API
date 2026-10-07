package features

import (
	"context"
	"encoding/json"

	"scriptbridge/internal/bridge"
)

func Register(b *bridge.Bridge) {
	b.Handle("app.echo", func(_ context.Context, raw json.RawMessage) (any, error) {
		var data struct {
			Text *string `json:"text"`
		}
		if json.Unmarshal(raw, &data) != nil || data.Text == nil {
			return nil, &bridge.Error{Code: "INVALID_DATA", Message: "Expected {text: string}"}
		}
		return map[string]string{"text": *data.Text}, nil
	})
}
