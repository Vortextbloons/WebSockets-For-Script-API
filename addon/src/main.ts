import { createMinecraftBridge } from "script-api-bridge/minecraft";
import { registerFeatures } from "./features";

const namespace = "scaffold";
const { bridge } = createMinecraftBridge({ namespace, prefix: "§8[SC] " });
registerFeatures(bridge, namespace);
