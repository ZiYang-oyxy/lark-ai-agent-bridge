# Existing Topic Session Stability

## Problem

In topic mode, a message can arrive as the root of an already-created Feishu
topic with a real `omt_*` thread ID. Bridge correctly starts that message on the
real thread key. Follow-up messages also carry `root_id`, however, and the
current router treats every `root_id` as proof that Bridge previously minted an
`@bot:<root_id>` key. It therefore moves the follow-up to a second Bridge and
Claude session and persists the wrong alias.

The captured production trace demonstrates the split:

- Root: `thread:omt_*`, no resumed Agent session.
- Follow-up: `thread:@bot:<root message id>`, no resumed Agent session.
- Alias store: the real `omt_*` is rebound to the synthetic key.

## Decision

Session state, not message shape, decides whether a synthetic fallback is
valid.

For a message with a real thread ID, routing uses this priority:

1. If the real-thread Bridge session already exists, keep using it. This also
   heals an alias polluted by the old behavior.
2. Otherwise, use a persisted alias when it points to the synthetic session
   created for the original top-level mention.
3. If the alias is absent but `@bot:<root_id>` actually exists, use that key as
   restart recovery.
4. Otherwise, use the real thread ID.

The pure topic-key function no longer infers a synthetic session from
`root_id` alone. The service-scoped resolver owns the state-dependent recovery.

## Alternatives Rejected

- Remove `root_id` recovery entirely: simple, but weakens recovery when the
  alias store is missing while the synthetic session still exists.
- Infer provenance from Feishu payload fields: manual topics and
  Bridge-created topics share the same follow-up shape, so this remains a
  heuristic and can split again.

## Verification

- Existing real session plus polluted alias resolves to the real session.
- Existing synthetic session plus missing alias resolves through `root_id`.
- Alias-only and normal unaliased topics retain their previous behavior.
- L1 runs the complete Go suite; L2 replays the captured root/follow-up routing
  shape; L3 verifies a real manually-created Test topic keeps one Claude
  session across root and follow-up messages.
