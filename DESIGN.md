---
name: OCQ native task panel
description: Compact task-first Herdr sidebar; review pending.
typography:
  title:
    fontSize: "16px"
    fontWeight: 600
  body:
    fontSize: "13px"
  detail:
    fontSize: "12px"
rounded:
  control: "5px"
spacing:
  tight: "4px"
  control-inline: "8px"
  panel: "12px"
components:
  button:
    rounded: "{rounded.control}"
    padding: "5px 8px"
---

# Design System: OCQ native task panel

## Overview

Native, task-first operation beside Herdr Files/Changes. The task and available
action lead; protocol evidence unfolds in place. This record describes the
current source and isolated native render, not independent design approval.

## Colors

Inherit `theme()`; never freeze the user's theme into copied hex values.
`background`/`foreground` provide the working surface, `muted` secondary facts,
`border` separators and `hover` selection/focus. `accent` marks the local primary
setup action. `blocked` marks unavailable authority, not ordinary empty content.

## Typography

Use Herdr's native family. Body 13 logical pixels, details/status 12, empty-state
heading 16 semibold. No display face, decorative mono or microtext.

## Layout

Default 360px panel; isolated renders also inspect 320px. Header: identity and
Details, then Work/Queue/Inbox and observation status. Recovery is one inline
strip. Task body owns the remaining scroll area. Header diagnostic content is
bounded to 260px and compatibility detail to 120px, with native scrolling.

## Elevation & Depth

Flat native surfaces, one-pixel dividers, no added shadows or animation.

## Shapes

Controls use 5px corners and existing Herdr hover/selection treatment.
Use rows and native text controls, not nested cards or invented diagrams.

## Components

- **Navigation:** Work first; Queue and Inbox remain one action away. Project
  summary is under Details; project switching is an explicit Change disclosure.
- **Recovery:** concise paused state, reconnect action, exact reason on demand.
  Observed data and action authorization are separate facts.
- **Empty project:** explanation, guarded local registration, setup help on demand.
  Copying help never starts Main.
- **Tasks:** exact request identity remains visible; friendly kind/state labels
  are presentation only. Recorded is not execution success.
- **Main/clients:** short summary, technical identity/history disclosures. A new
  client remains explicitly new; an unverified route is never presented as bound.
- **Confirmations:** preserve exact retained-byte review, page gates, original
  context checks, uncertainty warnings and pointer-only final confirmation.

## Do's and Don'ts

- Do keep timestamps, full pins and protocol explanations in Details.
- Do preserve disabled, unknown, stale and terminal-unproven distinctions.
- Don't interpret transport status as agent liveness or mutation permission.
- Don't populate empty screens with invented projects or replace user workspace
  state to manufacture a prettier demonstration.
