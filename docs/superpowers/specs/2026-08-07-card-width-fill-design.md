# CardKit Responsive Full Width

## Goal

Make every Bridge CardKit 2.0 card use the full width available in the Feishu
conversation content area so Markdown tables have more horizontal space.

## Design

- Add `width_mode: fill` to the root `config` emitted by `BuildLarkCard`.
- Apply the setting consistently to streaming replies, terminal replies, and
  command cards. A stable global width avoids layout jumps while a streamed
  Markdown table is still incomplete.
- Keep Markdown rendering, card padding, pagination, and native CardKit update
  behavior unchanged.
- Do not parse Markdown tables or convert them to the native `table` component.
  That would add parsing and streaming complexity without being required to
  solve the width constraint.

## Compatibility

`fill` is a Card 2.0 root width mode. Feishu remains responsible for the
responsive content-area boundary and client margins. Mobile clients continue
to fit the card to their available conversation width.

## Validation

- Unit tests lock the root `config.width_mode` value for representative
  streaming, terminal, Markdown-layout, and command cards.
- L1 runs the full Go suite.
- L2 checks the simulated Bridge event path remains unchanged.
- L3 sends a real wide Markdown table through the Mac Test bot, checks the
  CardKit create/reply audit chain, reads the real reply, and visually verifies
  that the card fills the desktop conversation content area.
