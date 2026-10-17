import { createMinecraftBridge } from "script-api-bridge/minecraft";
import { registerFeatures } from "./features";

const namespace = "scaffold";
const connection = createMinecraftBridge({ namespace, prefix: "§8[SC] " });
registerFeatures(connection, namespace);
