import { defineContract, schema } from "script-api-bridge";

export const contract = defineContract({
  requests: {
    "game.time": { request: schema.null(), response: schema.object({ "ticks": schema.number() }) },
    "app.echo": { request: schema.object({ "text": schema.string() }), response: schema.object({ "text": schema.string() }) },
  },
  events: {
    "game.player_joined": schema.object({ "name": schema.string() }),
  },
});
