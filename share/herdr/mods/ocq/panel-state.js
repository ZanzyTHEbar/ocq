import { layout, shared } from "herdr";

export const panelEpoch = () => shared("ocq.panel.epoch") ?? 0;

export function panelVisible() {
  function visit(node, visible) {
    if (!node) return null;
    visible = visible && !node.hidden;
    if (node.id === "ocq-panel") return visible;
    for (const child of node.children ?? []) {
      const found = visit(child, visible);
      if (found !== null) return found;
    }
    return null;
  }
  // Standalone OCQ mod panes/overlays do not have this personal layout block.
  return visit(layout(), true) !== false;
}
