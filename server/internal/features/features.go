package features

import (
	"context"

	"scriptbridge/bridge"
)

func Register(b *bridge.Bridge) {
	bridge.HandleTyped(b, AppEcho, func(_ context.Context, req AppEchoRequest) (AppEchoResponse, error) {
		return AppEchoResponse{Text: *req.Text}, nil
	})
}
