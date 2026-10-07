import { createBridge, startBridge } from "./bridge/minecraft";
import { registerFeatures } from "./features";

const bridge = createBridge();
registerFeatures(bridge);
startBridge(bridge);
