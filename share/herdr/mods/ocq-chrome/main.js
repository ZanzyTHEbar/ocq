// Derived from herdrdev/herdr-app v0.1.15 defaults/mods/tabs/main.js.
// SPDX-License-Identifier: Apache-2.0
// Modified for OCQ: adjacent panel toggle, per-space visibility and keyboard.
// Native tabs/Files/Changes/window controls retain their upstream behavior.
import { View, div, svg, image } from "gpui-kit";
import { h_flex, ScrollHandle } from "gpui-base";
import { spaces, panes, run, openOverlay, theme, window, shared, setShared, layout, settings, nextCommand } from "herdr";

function find(node, id) {
  if (node?.id === id) return node;
  for (const child of node?.children ?? []) {
    const found = find(child, id);
    if (found) return found;
  }
  return null;
}

export default class OCQChrome extends View {
  init(_props, cx) {
    this.pressed = null;
    this.scroll = ScrollHandle.new();
    this.panels = JSON.parse(localStorage.getItem("panels") ?? "{}");
    this.ocqPanels = JSON.parse(localStorage.getItem("ocq-panels") ?? "{}");
    this.handle = cx.focus_handle();
    cx.spawn(async cx => {
      for (;;) {
        const command = await nextCommand();
        if (command === "focus") this.handle.focus();
        if (command === "toggle") { this.toggleOCQ(); cx.notify(); }
      }
    });
  }

  // Native Files/Changes selection is still independent of OCQ visibility.
  syncPanel(space, panel, body, enabled) {
    if (!space || !panel) return;
    const now = panel.hidden ? null : body?.active ?? null;
    if (space.id !== this.space) {
      const first = this.space === undefined;
      this.space = space.id; this.pending = null;
      if (!(first && !(space.id in this.panels))) {
        const stored = this.panels[space.id] ?? null, want = enabled.includes(stored) ? stored : null;
        if (want !== now) {
          this.pending = { want, until: Date.now() + 1000 };
          if (want) { run("layout.select", { stack: "panel-body", child: want }); run("layout.show", { id: "panel" }); }
          else run("layout.hide", { id: "panel" });
          return;
        }
      }
    }
    if (this.pending) {
      if (this.pending.want !== now && Date.now() < this.pending.until) return;
      this.pending = null;
    }
    if (this.panels[space.id] === now) return;
    this.panels[space.id] = now;
    localStorage.setItem("panels", JSON.stringify(this.panels));
  }

  visibilityChanged() {
    setShared("ocq.panel.epoch", (shared("ocq.panel.epoch") ?? 0) + 1);
  }

  showOCQ(shown) {
    // Invalidate old confirmations synchronously, before layout/async updates.
    this.visibilityChanged();
    this.ocqShown = shown;
    run(shown ? "layout.show" : "layout.hide", { id: "ocq-panel" });
  }

  syncOCQ(space, panel) {
    if (!space || !panel) return;
    const shown = !panel.hidden;
    if (this.ocqSpace !== space.id) {
      const first = this.ocqSpace === undefined;
      this.ocqSpace = space.id;
      const saved = this.ocqPanels[space.id];
      // Keep Herdr's persisted layout on the first unrecorded space.
      const want = typeof saved === "boolean" ? saved : first ? shown : true;
      if (want !== shown) { this.showOCQ(want); return; }
      this.ocqShown = shown;
    }
    if (this.ocqShown !== shown) { this.visibilityChanged(); this.ocqShown = shown; }
    if (this.ocqPanels[space.id] !== shown) {
      this.ocqPanels[space.id] = shown;
      localStorage.setItem("ocq-panels", JSON.stringify(this.ocqPanels));
    }
  }

  toggleOCQ() {
    const panel = find(layout(), "ocq-panel");
    const space = spaces().find(item => item.active);
    if (!panel || !space) return;
    this.ocqPanels[space.id] = panel.hidden;
    localStorage.setItem("ocq-panels", JSON.stringify(this.ocqPanels));
    this.showOCQ(panel.hidden);
  }

  render() {
    const t = theme();
    const space = spaces().find(p => p.active);
    const shown = space?.tabs ?? [];
    const active = shown.findIndex(tab => tab.active);
    const selected = shown[active]?.id;
    if (selected !== this.selected || active !== this.selectedIndex) {
      this.selected = selected;
      this.selectedIndex = active;
      if (active >= 0) this.scroll.scroll_to_item(active);
    }
    const stop = (_e, cx) => cx.stop_propagation();
    const button = (id, label, action) => div().id(id).size(26).flex_none().flex().items_center().justify_center().rounded(6).text_color(t.dim).hover(s => s.bg(t.hover).text_color(t.foreground))
      .on_mouse_down("left", stop).on_click(action).child(label);
    const item = (c, i) => {
      const program = panes().find(p => p.tab === c.id && p.program?.agent)?.program ?? panes().find(p => p.tab === c.id)?.program;
      const name = /^tb\d+$/.test(c.name) ? program?.name ?? "Terminal" : c.name;
      return h_flex().id(`tab-${c.id}`).h(29).px(10).gap(7).rounded(7).items_center().flex_none().text_size(12.8)
        .when(i <= active, el => el.on_size_change((_size, cx) => { this.scroll.scroll_to_item(active); cx.notify(); }))
        .border(1).border_color(c.active ? t.border : t.chrome).bg(c.active ? t.raised : t.chrome).text_color(c.active ? t.foreground : t.muted)
        .hover(s => s.bg(t.hover))
        .on_mouse_down("left", (e, cx) => { cx.stop_propagation(); this.pressed = i; if (e.click_count === 2) run("tab.rename", { id: c.id }); })
        .on_mouse_up("left", () => {
          const from = this.pressed; this.pressed = null;
          if (from !== null && from !== i) run("tab.move", { id: shown[from].id, index: space.tabs.findIndex(x => x.id === c.id) + 1 });
          else run("tab.select", { id: c.id });
        })
        .on_mouse_down("right", (_e, cx) => { cx.stop_propagation(); openOverlay({ mod: "menu", kind: "popup", width: 200, props: { tab: "tab", target: { id: c.id, space: space.id } } }); })
        .when(Boolean(program?.icon), el => el.child(program.tint ? svg(program.icon).size(14).text_color(t.muted) : image(program.icon).size(14)))
        .child(div().max_w(170).truncate().child(c.zoomed ? `${name} Z` : name))
        .when(c.status === "working", el => el.child(svg("mods/workspace-chrome/spinner.svg").size(12).flex_none().text_color(t.working).spin(900)))
        .when(c.status === "blocked" || c.status === "done", el => el.child(div().size(6).rounded(3).bg(t[c.status])))
        .when(c.active, el => el.child(div().id(`close-${c.id}`).text_color(t.dim).on_mouse_down("left", stop).on_click((_e, cx) => { cx.stop_propagation(); run("tab.close", { id: c.id }); }).child(svg("mods/pane/close.svg").size(12))));
    };
    const root = layout(), sidebar = find(root, "sidebar"), w = window();
    const lightsPadding = w?.platform === "macos" && w.titlebar === "none" && !w.fullscreen && root?.traffic_lights
      ? Math.max(10, (root.traffic_lights[0] + 64) / settings().zoom - (sidebar?.hidden ? 0 : sidebar?.size?.px ?? 0))
      : 10;
    const panel = find(root, "panel"), body = find(root, "panel-body"), ocq = find(root, "ocq-panel");
    const enabled = shared("workspace.panels") ?? ["files", "changes"];
    this.syncPanel(space, panel, body, enabled);
    this.syncOCQ(space, ocq);
    const panelButton = (id, label, icon) => {
      const on = panel && !panel.hidden && body?.active === id;
      return h_flex().id(`panel-${id}`).h(26).px(8).gap(5).flex_none().items_center().rounded(6)
        .text_size(12).bg(on ? t.selected : t.chrome).text_color(on ? t.foreground : t.muted)
        .hover(s => s.bg(t.hover)).on_mouse_down("left", stop)
        .on_click(() => {
          if (on) run("layout.hide", { id: "panel" });
          else { run("layout.select", { stack: "panel-body", child: id }); run("layout.show", { id: "panel" }); }
        }).child(svg(icon).size(14)).child(label);
    };
    return h_flex().size_full().min_w(0).px(10).pl(lightsPadding).gap(3).items_center().bg(t.chrome).border_b(1).border_color(t.border)
      .track_focus(this.handle).on_key_down((event, cx) => {
        if (!event.is_held && !event.modifiers.control && !event.modifiers.alt && !event.modifiers.platform && ["enter", "space"].includes(event.key)) {
          this.toggleOCQ(); cx.stop_propagation(); cx.notify();
        }
      })
      .on_mouse_up("left", () => { this.pressed = null; })
      .on_scroll_wheel(e => { if (Math.abs(e.delta.y) >= 1) run(e.delta.y > 0 ? "tab.prev" : "tab.next"); })
      .child(h_flex().id("tab-scroll").flex_1().min_w(0).h_full().items_center().gap(3).overflow_x_scrollbar().track_scroll(this.scroll)
        .on_size_change((_size, cx) => { if (active >= 0) { this.scroll.scroll_to_item(active); cx.notify(); } })
        .children(shown.map(item)))
      .child(button("tab-new", "+", () => openOverlay({ mod: "new-menu", kind: "popup", width: 250, height: 512 })))
      .when(enabled.includes("files"), el => el.child(panelButton("files", "Files", "mods/files/file.svg")))
      .when(enabled.includes("changes"), el => el.child(panelButton("changes", "Changes", "mods/changes/changes.svg")))
      .when(Boolean(ocq), el => el.child(h_flex().id("panel-ocq").role("button")
        .accessibility_label(ocq?.hidden ? "Show OCQ panel" : "Hide OCQ panel")
        .h(26).px(8).gap(5).flex_none().items_center().rounded(6).text_size(12)
        .bg(ocq?.hidden ? t.chrome : t.selected).text_color(ocq?.hidden ? t.muted : t.foreground)
        .cursor_pointer().hover(s => s.bg(t.hover)).tooltip("Toggle OCQ panel · Enter or Space when focused")
        .on_mouse_down("left", stop).on_click((_event, cx) => { this.handle.focus(); this.toggleOCQ(); cx.notify(); })
        .child(svg("mods/workspace-chrome/sidebar.svg").size(14)).child("OCQ")))
      .when(w?.buttons, el => el.child(button("minimize", w.platform === "windows" ? svg("mods/tabs/win-min.svg").size(16) : "−", () => run("window.minimize")))
        .child(button("maximize", w.platform === "windows" ? svg(`mods/tabs/${w.maximized ? "win-restore" : "win-max"}.svg`).size(16) : "□", () => run("window.maximize")))
        .child(button("close-window", w.platform === "windows" ? svg("mods/tabs/win-close.svg").size(16) : svg("mods/pane/close.svg").size(13), () => run("window.close"))));
  }
}
