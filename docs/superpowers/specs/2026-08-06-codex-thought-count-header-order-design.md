# Codex Thought Count And Header Order

## Goal

- Count each non-final Codex `agent_message` shown in the reasoning timeline as
  one thought round.
- Preserve native reasoning delta aggregation and Claude snapshot behavior.
- Render Worker metrics in the order `elapsed -> thoughts -> tools` in running
  and terminal headers.

## Event Contract

Codex keeps its latest complete `agent_message` visible as an answer candidate.
When later activity proves that candidate is progress rather than the final
answer, the parser emits the same text as a `SegmentThought` with
`ProgressSnapshot=true` and `AssistantSnapshot=false`.

`ProgressSnapshot` is the process-round boundary. The clean-card stream uses it
to finalize and count that thought without treating it as an ordered assistant
answer snapshot. This separation preserves Coder inline history while fixing
Worker's cumulative count.

Native `reasoning` items and Claude assistant snapshots keep their existing
aggregation rules. No count is added for empty or duplicate tool frames.

## Card Header

Worker running and terminal cards use:

`<status> · ⏱ <elapsed> · 💭 <thoughts> · 🔧 <tools>`

Coder and Singleton retain their existing header formats.

## Validation

- Parser regression: a demoted Codex `agent_message` carries
  `ProgressSnapshot=true` without `AssistantSnapshot` or `AnswerSnapshot`.
- Stream integration: two Codex progress messages and two tools produce
  `ThoughtRoundCount=2`, `ToolRoundCount=2`, and the expected running/terminal
  header order.
- Compatibility: existing Claude snapshot, native reasoning, append mode, and
  latest-card tests remain green.
- L1 runs the full Go suite. L2 exercises the controlled Codex JSONL-to-card
  path. L3 checks a real Codex Worker card on the ephemeral Mac Test bot.

## Risks

- Reusing `AssistantSnapshot` would also trigger ordered-answer replacement, so
  the fix must use only `ProgressSnapshot` for Codex demotion.
- Counting every `SegmentThought` would overcount incremental reasoning deltas;
  only explicit process boundaries may increment the counter.
