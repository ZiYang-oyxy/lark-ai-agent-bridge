# Developer Status Bar Channel IDs

## Goal

When Developer Mode is enabled, expose the current Feishu conversation IDs on
the third status-bar row so operators can diagnose chat and topic routing from
the reply card itself.

## Design

- Keep the existing three-row status-bar layout and visibility preferences.
- Add `ChatID` and `TopicID` to card metadata.
- Populate `ChatID` from the durable session key and `TopicID` from the inbound
  Feishu `thread_id` stored with the durable input.
- On the developer row, append `Chat ID` and `Topic ID` only when
  `DeveloperMode` is true.
- Compact long IDs for display by preserving the type prefix plus the first
  three and last six identifier characters, for example `oc_f56…296b83`.
- Render an empty Topic ID as `-`. Never display the synthetic
  `@bot:<message_id>` routing key as a Feishu Topic ID.
- Stable mode keeps the existing developer-row output unchanged.

## Data Flow

1. Message intake stores the real inbound `thread_id` as `Input.TopicID`.
2. Run metadata combines `Session.Key.ChatID` and `Input.TopicID`.
3. CardKit and Markdown renderers reuse `card.MetaRows`, so both reply paths
   receive identical output.

## Compatibility

`Input.TopicID` is additive and uses `omitempty`, so older session snapshots
remain readable. Existing visibility settings, routing keys, and session IDs do
not change.

## Validation

- Unit tests cover developer mode with both IDs, root-chat fallback, and stable
  mode hiding the IDs.
- Bridge tests prove intake-to-run metadata propagation.
- L1 runs the full Go suite; L2 inspects simulated card output; L3 reads a real
  Test-bot CardKit reply in both root-chat and topic scopes.
