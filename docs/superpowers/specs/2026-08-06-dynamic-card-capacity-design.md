# Dynamic Card Capacity

## Goal

Use as much of Feishu's documented card capacity as practical in every reply
mode without treating a rune count as the platform limit.

Feishu documents a 30 KB card-content limit and a 200-component limit for
CardKit JSON 2.0. The bridge will target 29 KiB of compact serialized card JSON
and retain the existing 200-component check. The remaining space is a safety
margin for differences in how the platform accounts for card content.

Official references:

- https://open.feishu.cn/document/cardkit-v1/card/create
- https://open.feishu.cn/document/server-docs/im-v1/faq
- https://open.feishu.cn/document/cardkit-v1/streaming-updates-openapi-overview

## Capacity Model

The bridge will use two separate limits:

1. `CardMaxChars`, defaulting to 30000 runes, bounds the candidate text window
   retained in memory and inspected by renderers. It is not a Feishu limit.
2. `LarkCardSoftMaxJSONBytes`, set to 29 KiB, bounds the compact JSON returned
   by `BuildLarkCard`. This is the authoritative per-card payload budget.

Every prepared card must satisfy both:

- serialized JSON length at most 29 KiB;
- component and element count at most 200.

The byte-based fitter remains responsible for choosing the largest candidate
that satisfies these conditions. Rune limits must not be used as a proxy for
card capacity.

## Reply Modes

### Coder

Remove the fixed 9000-rune inline-timeline ceiling. Retain at most
`CardMaxChars` candidate runes, preserve the newest timeline tail, then let
`PrepareLarkCard` maximize the timeline against the 29 KiB JSON budget.

The omission notice appears only when the candidate window or serialized card
actually cannot retain the older timeline. It must not appear merely because
the old 9000-rune threshold was crossed.

### Worker And Singleton

Both clean-card modes retain at most `CardMaxChars` candidate runes. Their
three-section card is fitted as one payload:

1. preserve the latest answer;
2. preserve the newest-first structure and headers of the visible thought and
   tool records;
3. shrink verbose thought and tool bodies when the combined JSON is oversized;
4. use any remaining capacity for answer content.

The existing two-record visibility policy for thought and tool timelines is
unchanged. This change increases content capacity, not history depth.

### Coder Continuation

Replace the fixed 6000-rune page size with `CardMaxChars` as the candidate page
window. For every page, binary-search the largest prefix whose complete card
JSON fits within 29 KiB and 200 components.

The maximum remains nine cards. When the aggregate nine-card window is full,
retain a continuous newest tail and place the omission notice on the first
retained page.

## Configuration

- Change the default `E2E_CARD_MAX_CHARS` from 12000 to 30000.
- Keep the environment override as a candidate-window control for operators.
- Standard Coder, Worker, and Singleton previews derive their candidate limit
  from `E2E_CARD_MAX_CHARS`; they do not use the legacy 2000-rune preview cap.
- Keep `E2E_CARD_PREVIEW_MAX_CHARS` for compatibility with non-standard or
  fallback preview paths. Do not present it as the platform card capacity.
- Update CLI help to describe `E2E_CARD_MAX_CHARS` as the candidate rune
  window. Add `card_payload_max_json_bytes=29696` to `doctor` output so runtime
  diagnostics show both limits and they cannot be confused again.

## Failure Handling

`PrepareLarkCard` remains the only path that produces CardKit payloads. If the
initial card is oversized, the existing mode-specific binary fit runs before
the payload reaches the Feishu client. If no useful representation fits, use
the existing emergency card.

The change does not use platform error `200860` as normal control flow and does
not add a retry after a rejected write. A real `200860` remains an audited
failure because it means the local capacity calculation and platform behavior
have diverged.

Redaction must run before payload measurement. Increasing the candidate window
must not bypass `security.Redact` or allow a later redaction pass to expand the
serialized payload after fitting.

## Compatibility

- Existing explicit `E2E_CARD_MAX_CHARS` values remain valid: values below the
  new default reduce the candidate window, while larger values widen only the
  candidate window and cannot bypass the 29 KiB payload budget.
- Card JSON schema, component IDs, reply-mode names, and persisted preferences
  do not change.
- Terminal card structure and streaming update semantics do not change.
- Text-message limits are separate from CardKit capacity and retain their
  existing send-path validation.

## Validation

### L1

- Assert the default candidate window is 30000 runes and the prepared JSON
  budget is 29 KiB.
- Use ASCII, Chinese, emoji, Markdown-heavy, and escaped content to prove that
  payload size, rather than rune count, determines retained content.
- Prove Coder keeps more than 9000 ASCII runes when the card still fits.
- Prove Worker and Singleton keep more than 12000 ASCII runes when their full
  three-section card still fits.
- Prove oversized CJK and Markdown-heavy cards remain at or below 29 KiB.
- Prove every continuation page independently approaches but never exceeds the
  byte and component budgets, with no more than nine pages.
- Run `go test ./...`, `go vet ./...`, and `git diff --check`.

### L2

Run local simulations for Coder, Worker, Singleton, and Coder continuation.
Assert stream-to-result convergence, the expected layout flags, and no failed
or rejected audit events.

### L3

Use the ephemeral Mac Test bot with a real Codex run and real Feishu cards.
For each standard reply mode, read a running card containing more than its old
rune threshold and verify that later content remains visible without an early
omission marker or `[truncated]`. Verify terminal convergence, restore Test
preferences, release the lease, and confirm the supervisor PID is unchanged.
