package features

import "errors"

const GameTime = "game.time"

type GameTimeResponse struct {
	Ticks float64 `json:"ticks"`
}

const AppEcho = "app.echo"

type AppEchoRequest struct {
	Text *string `json:"text"`
}

func (r AppEchoRequest) Validate() error {
	if r.Text == nil {
		return errors.New("Missing text")
	}
	return nil
}

type AppEchoResponse struct {
	Text string `json:"text"`
}

const GamePlayerJoined = "game.player_joined"

type GamePlayerJoinedEvent struct {
	Name string `json:"name"`
}
