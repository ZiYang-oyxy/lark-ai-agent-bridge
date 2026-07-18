# Real Feishu E2E Workflow

This workflow is for real Feishu/Lark integration tests. It sends messages to the configured group, starts a real bridge process, and validates user-visible CardKit behavior through Feishu APIs and audit logs.

It is intentionally not part of `go test ./...` or `./scripts/verify.sh`.

## Scope

The E2E suite verifies the bridge as a real user would use it:

- Feishu long connection receives group messages and card action callbacks.
- User messages are sent by local `lark-cli --as user`.
- The bridge replies with CardKit run cards.
- Claude runs in one-shot mode.
- Card updates stream while Claude is running.
- Message revoke interactions are performed with the current `lark-cli --as user` identity against real Feishu messages.
- Stop and workdir button interactions are validated through historical Chrome MCP runs and local CardKit/action tests; the repository no longer keeps a self-written Chrome/CDP helper.
- Evidence is written locally for later debugging.

Media file E2E uses the existing P2P chat between the current `lark-cli` user and this bot. This avoids the invalid assumption that a Feishu `post` can contain a `{tag:file}` element. The separate `debounce_dm` case still uses `--user-id "$BOT_OPEN_ID"`; if that app-domain identifier is incompatible, that case remains an explicit nonzero external prerequisite rather than falling back to group chat.

## Developer-local profiles

Every developer must use their own Feishu app, bot and test group. Bootstrap a named profile:

```bash
./scripts/e2e-init.sh --profile personal
```

The command writes only to gitignored local paths:

```text
.lark-agent-bridge/e2e/profiles/personal.env
.lark-agent-bridge/e2e/profiles/personal.json
.lark-agent-bridge/e2e/locks/personal.lock/
.cache/e2e/personal/<run-id>/
```

The env and metadata files are `0600`; their directories are `0700`. They contain real app/bot/chat identity and must never be added to Git, copied into tracked documentation, or pasted into reports.

Bootstrap also creates a named `lark-cli` profile, defaulting to `lab-e2e-<profile>`, without switching the CLI global default. Every auth, chat, message, reply, recall and media command in named-profile runs is forced through that isolated CLI profile. This prevents one developer or concurrent session from replacing another developer's app binding or OAuth token.

One checkout may contain multiple profiles:

```bash
./scripts/e2e-init.sh --profile personal
./scripts/e2e-init.sh --profile staging
./scripts/e2e-real.sh --profile personal --doctor
E2E_E2E_PROFILE=staging ./scripts/e2e-real.sh --doctor
```

Selection order is explicit `--profile`, `E2E_E2E_PROFILE`, the only named profile, then legacy `.lark-agent-bridge/e2e.env`. If multiple named profiles exist, the runner refuses to guess.

Different profiles may run concurrently. An active preflight or Feature run holds a local profile-scoped lock; a second run of the same profile is `BLOCKED:profile_busy`. This lock protects one machine only. Developers must not share one bot across machines unless they coordinate outside this harness.

The legacy `.lark-agent-bridge/e2e.env` remains readable as the `legacy` profile, but new setup should use `e2e-init.sh`.

## Profile fields

Required values are stored without `export` in the local profile env:

```bash
LARK_APP_ID=cli_xxx
LARK_APP_SECRET=xxx
LARK_BOT_OPEN_ID=ou_xxx
E2E_E2E_CHAT_ID=oc_xxx
E2E_E2E_LARK_CLI_PROFILE=lab-e2e-personal
```

Optional:

```bash
E2E_REAL_E2E_TIMEOUT_SEC=420
E2E_REAL_E2E_P2P_CHAT_ID=oc_xxx
E2E_REAL_E2E_DEFAULT_WORKDIR=/tmp/lark-agent-bridge-real
E2E_REAL_E2E_FAKE_CLAUDE=1
E2E_REAL_E2E_CALLBACK_ADDR=127.0.0.1:28080
E2E_CLAUDE_BIN=/absolute/path/to/claude
```

`LARK_BOT_OPEN_ID` is optional. If it is omitted, `scripts/e2e-real.sh` queries `bot/v3/info` with the bridge app token. The script must never print app secret or tenant token.
`E2E_REAL_E2E_CALLBACK_ADDR` is optional; it pins the local callback port used only by the `stop_preserves_queue` case. Without it, the script selects a loopback port for that run.
`E2E_REAL_E2E_P2P_CHAT_ID` is optional at bootstrap and required by native media file cases. `debounce_dm` can create the direct chat by sending through the bot open_id and does not require a pre-existing chat ID. Bootstrap paginates the user's P2P chats before matching bot membership; if no unique match exists, it still creates a group-only profile and reports a `NOTICE`. It must never substitute a group chat or another similarly named bot.
When `--p2p-chat-id` is supplied to `e2e-init.sh`, bootstrap verifies that the selected chat contains the resolved bot before saving it.
`E2E_E2E_LARK_CLI_PROFILE` identifies the local named CLI configuration. If the name already exists for a different App ID, bootstrap returns `BLOCKED:lark_cli_profile_mismatch` and never overwrites or switches it. Bootstrap and doctor validate the complete E2E user-scope bundle once: send, group/P2P message observation, reaction observation, recall, resource access, and chat/member reads. A missing member produces `BLOCKED:user_e2e_scopes_missing` with one copy-pasteable authorization command.

## Feishu App Prerequisites

The bridge app should use long connection mode for both message events and card callbacks. No public HTTP callback URL is required for these E2E tests.

Required event subscriptions:

- `im.message.receive_v1`
- `card.action.trigger`
- `im.message.recalled_v1` for prompt revoke cancellation.

The developer's user OAuth scope bundle is distinct from the bot's tenant permissions. Bootstrap and `--doctor` verify all of the following before an active run:

```bash
im:message
im:message.group_msg:get_as_user
im:message.p2p_msg:get_as_user
im:message.reactions:read
im:message:recall
im:resource
im:chat:read
im:chat.members:read
```

If the App has not enabled and published every scope in this bundle, authorization cannot grant it. Enable and publish the missing App permissions, then run the exact command printed by bootstrap or doctor. Repeated OAuth login without enabling the App permission does not fix it.

Recall is an external subscription dependency, not a bridge-generated event. A real run must show a new `message_recalled_*` audit line after each delete. The first Core Task 9 window in `.cache/evidence/1c7d3bf/core-real/` received no recall event at all, so `recall_state` correctly failed instead of treating deletion success as delivery. Check the app's published event subscription/version and tenant installation before rerunning recall cases.

Recommended existing subscriptions for diagnostics:

- `im.message.message_read_v1`
- `im.message.reaction.created_v1`
- `im.message.reaction.deleted_v1`

Current known permission note: `im:message.group_msg` is only needed for proactively listing group message history. The bridge main path receives group messages through long connection events and does not depend on group history listing.

## Local Prerequisites

Install and verify:

```bash
go version
jq --version
lark-cli --version
claude --version
python3 -c 'from PIL import Image; print(Image.__version__)'
```

The `lark-cli` user identity must be logged in and able to send a text message to `E2E_E2E_CHAT_ID`:

```bash
lark-cli im +messages-send --as user \
  --chat-id "$E2E_E2E_CHAT_ID" \
  --text "lark-agent-bridge e2e probe"
```

For full reliability mode, set `E2E_REAL_E2E_FAKE_CLAUDE=1`. The durable restart, batching, queue-capacity and scope-parallel cases require it so they can assert a deterministic child-process argv and lifecycle without spending model tokens. The fake Claude only affects the bridge process started by the E2E script; Feishu message delivery, long connection events, CardKit create/update, audit logging, and message revoke events are still real.

The five media cases also require fake Claude. They assert exact accepted cache paths in the child prompt and prove rejected paths never start or reach a child. Upload, long-connection delivery, tenant-token resource download, cache validation, CardKit replies, and `mget` verification remain real.

## Commands

List cases:

```bash
./scripts/e2e-real.sh --list-cases
```

Run static capability checks without sending messages or starting a bridge:

```bash
./scripts/e2e-real.sh --profile personal --doctor
```

Run static checks plus real group, DM, CardKit, recall and media canaries with fake Claude:

```bash
./scripts/e2e-real.sh --profile personal --preflight-only
```

Capability states are:

- `PASS`: directly proven.
- `FAIL`: harness, bridge or assertion failure; always exits nonzero.
- `BLOCKED`: external identity, permission, topology or subscription is missing.
- `SKIPPED`: an upstream prerequisite was not available.

Exit codes are `0` for no failures, `1` for any failure, `2` for CLI/profile errors and `3` for blocked strict runs. Development mode may continue unrelated cases when one capability is blocked. Use strict mode for a release gate:

```bash
./scripts/e2e-real.sh --profile personal --mode full --strict-capabilities
```

The latest active-canary result is cached inside the gitignored profile directory. A Feature run rechecks current static prerequisites before using a cached active capability. If every selected case is already blocked, the runner writes capability/case evidence and exits without building or starting the bridge.

Run smoke cases:

```bash
./scripts/e2e-real.sh --profile personal --mode smoke
```

Run full cases:

```bash
./scripts/e2e-real.sh --profile personal --mode full
```

Run selected cases:

```bash
./scripts/e2e-real.sh \
  --profile personal \
  --case workdir_existing \
  --case message_revoke_pending_workdir
```

Keep the bridge process alive after failure:

```bash
./scripts/e2e-real.sh --mode full --keep-server-on-fail
```

Without `--keep-server-on-fail`, a failed non-preflight case restarts the bridge before the next case. This clears any blocking fake child and pending scope state so one failure does not contaminate later evidence.

Use a custom evidence directory or default workdir:

```bash
./scripts/e2e-real.sh \
  --mode smoke \
  --run-dir .cache/e2e/manual-smoke \
  --default-workdir /tmp/lark-agent-bridge-manual
```

## Case Matrix

Smoke cases:

- `preflight`: runs local Feishu preflight checks.
- `new_basic`: sends `@bot /new ...` and verifies final CardKit result.
- `streaming_card`: verifies at least one streaming card update before result.
- `help`: verifies `/help`.
- `status`: verifies `/status`.
- `workdir_existing`: sends `/new --workdir <existing>` and verifies Claude `pwd`.
- `topic_reply_at`: replies in thread with `@bot` and verifies topic continuation.
- `topic_reply_without_at_negative`: replies in thread without `@bot` and verifies bridge ignores it.

Full-only cases:

- `message_revoke`: revokes the current `lark-cli` user's own active prompt message and verifies the next request is not blocked by a leaked run.
- `message_revoke_pending_workdir`: revokes a prompt waiting on workdir creation and verifies the workdir is not created and Claude does not start.
- `message_revoke_queued_input`: revokes a queued prompt and verifies it is not dequeued after the active run is stopped.
- `session_restart_context`: completes `/new` and a follow-up plain message in the same stable root chat scope across restart, then verifies the post-offset fake child invocation contains both the follow-up marker and `--resume fake-e2e-session`.
- `restart_queued_cancel`: kills a bridge with a queued input, verifies `session_recovery_cancelled`, proves the old marker did not start after restart, and verifies a fresh request completes.
- `restart_running_interrupted`: kills a bridge while a fake child is running, verifies `session_recovery_interrupted`, proves the old marker did not restart, and verifies a fresh request completes.
- `debounce_dm`: uses `--user-id "$BOT_OPEN_ID"` to concurrently send two real P2P plain messages, then verifies one post-offset fake invocation contains both markers. Its 250 ms quiet window starts at local bridge receipt time, matching LCAB's local timer behavior; platform `create_time` remains ordering metadata and cannot expire the debounce window before delivery. An invalid cross-app open_id is a recorded nonzero failure; there is no group fallback.
- `debounce_group`: concurrently sends two plain group messages without `/new` and verifies one post-offset fake invocation contains both markers plus the final CardKit result.
- `busy_merge`: queues two compatible plain inputs behind a running child, stops the active batch through loopback `stop_card`, and verifies the next fake child argv contains both queued markers and completes one final card.
- `queue_full`: restarts with `E2E_QUEUE_MAX_PENDING=2`, verifies `queue_rejected`, the rejection card text, that the rejected marker never starts a child process, then uses loopback `stop_card` so the accepted queued input can complete.
- `scope_parallel`: creates two thread scopes, reads both actual `thread_id` values through `mget`, verifies both blocking child processes start, and constructs exact thread-scoped card session ids for loopback stop cleanup.
- `stop_preserves_queue`: posts a real stop action to the bridge's local `/card/callback` compatibility endpoint, verifies `batch_stop_requested` and the stopped active card, then verifies the already queued input starts and reaches a final CardKit result. This endpoint is intentionally local to the E2E bridge process; production button delivery remains long connection `card.action.trigger`.
- `recall_state`: exclusively verifies real `im.message.recalled_v1` delivery by recalling a queued input and then its active input, requiring new offset-bounded recall audit states, the active stopped card, and no result card for the recalled queued marker. Missing subscription delivery is an expected external blocker and remains a nonzero failure.
- `media_attachment_only`: sends an attachment-only JPEG through an `@bot + img` post and verifies its SHA-256 cache path reaches the Agent prompt.
- `media_images`: sends JPEG/PNG/WebP/GIF in one real post and verifies all four accepted paths reach one Agent prompt.
- `media_text_files`: sends `.txt/.md/.json/.csv` as native P2P file messages and verifies each canonical cache path reaches the Agent prompt.
- `media_partial`: verifies mixed text+image succeeds, then verifies an unsupported peer file gets a user-visible failure without starting another Agent process.
- `media_rejected`: verifies forged image content, a 26 MiB file, PDF, DOCX, audio-as-file, and unknown binary all fail visibly and never reach the Agent prompt.
- `config_roundtrip`: opens the real `/config` card, saves default/sonnet/opus/haiku plus one custom allowed model across all four effort values, then verifies persistence, exact frozen argv and result-card metadata.
- `config_reset`: saves an override, resets it, restarts the bridge and verifies the persisted override stays absent while `E2E_MODEL`/`E2E_EFFORT` defaults drive the next run.
- `config_frozen_queue`: queues one input under sonnet/low, changes preferences to opus/high, queues another input and verifies the two later Agent invocations retain their enqueue-time values.
- `requested_actual_model`: verifies the result card distinguishes requested `opus` from fake CLI actual `fake-claude-e2e`, and requires the mismatch audit event.
- `wrapper_preflight`: runs `doctor --strict` with the E2E wrapper, verifies the bounded harmless argv and requires redacted successful output.

Run the media gate serially, with no other bridge process connected to the same app:

```bash
E2E_REAL_E2E_FAKE_CLAUDE=1 ./scripts/e2e-real.sh \
  --case media_attachment_only --case media_images --case media_text_files \
  --case media_partial --case media_rejected
```

Feishu file resources currently return `application/octet-stream` for ordinary files and `application/x-xls` for CSV. The bridge maps those transport declarations only after an allowlisted extension match, then still requires byte-level content sniffing. A failure card is polled through `mget` because the read API can lag the successful CardKit reply audit by a few seconds.

Run the runtime-configuration gate in a separate window after media and before reply-mode E2E. `fake Claude` keeps argv, queue boundaries and reported actual model deterministic; Feishu message delivery, `/config` CardKit rendering/callbacks and reply reads remain real:

```bash
E2E_REAL_E2E_FAKE_CLAUDE=1 \
E2E_MODEL=sonnet E2E_EFFORT=medium \
./scripts/e2e-real.sh \
  --case config_roundtrip --case config_reset --case config_frozen_queue \
  --case requested_actual_model --case wrapper_preflight
```

The gate also submits an invalid `model/effort` callback fixture and requires `config_save_failed` without changing the last valid persisted value. `config_reset` restarts the E2E bridge and proves the configured environment defaults regain precedence. Do not run another bridge process for the same Feishu app during this matrix.

The restart cases codify the durable contract exactly: context resumes, pending does not. `debouncing`, `queued`, and `starting` inputs become `cancelled`; `running` becomes `interrupted`; old commands are never automatically re-run, so the user must send a new message after restart.

The personal bridge deliberately has no global semaphore, FIFO, or fairness policy. The E2E suite checks only the intended boundary: serial execution within one chat/topic scope and parallel execution for distinct scopes.

## Evidence

Every run writes a directory like:

```text
.cache/e2e/<profile>/real-YYYYMMDD-HHMMSS/
```

Important files:

- `summary.md`: run metadata, case status, elapsed time, and paths.
- `capabilities.json`: machine-readable capability status, reason codes, remediation and local evidence references.
- `audit.jsonl`: bridge audit events for the run.
- `server.log`: bridge stdout/stderr.
- `messages.jsonl`: sent message ids and Feishu thread links.
- `mget/*.json`: `lark-cli im +messages-mget` snapshots.
- `<case>.log`: per-case command output.

When reporting a failure, include the case name, `summary.md`, the matching audit lines, and the mget snapshot.

## Known Issues To Regress

### Only `Get` Reaction After Revoking A Prompt

Observed failure mode:

1. User sends `@bot /new ...`.
2. User revokes the original prompt message after the bridge already receives it.
3. CardKit `CreateCard` may succeed, but `ReplyCard` can fail because the original message no longer exists.
4. If the bridge ignores `stream.Start()` failure, Claude may keep running in the background.
5. Later messages can be queued behind that leaked run and Feishu only shows the `Get` reaction.

The `stream.Start()` failure path is covered by local unit tests and should record `card_render_failed`, avoid starting Claude, clear active run state, and clear queued inputs. The post-reply revoke path is handled through long connection `im.message.recalled_v1`: pending workdir confirmations are cancelled, active runs are stopped, queued inputs are removed, and unknown/already-finished messages are audit-only.

Regression target:

- `message_revoke` should verify the leaked-run condition does not occur.
- Audit should clearly show `card_render_failed` for start-time render failures, or `message_recalled_pending_cancelled` / `message_recalled_active_cancelled` / `message_recalled_queued_cancelled` for post-reply message revokes.
- The next `@bot /new ...` should complete normally without `queue_input`.

### Workdir Falls Back To Default

Observed failure mode:

1. User sends `/new --workdir <path>`.
2. Confirmation card shows `<path>` correctly.
3. Later run card or follow-up query still runs in the bridge startup default workdir.

Regression target:

- `workdir_existing` verifies direct existing workdir execution.
- `workdir_persist_followup` verifies created workdir persistence across the same chat/topic.
- The run card footer `📁` and Claude `pwd`/`$PWD` must both match `<path>`.

## Triage Checklist

For failures, check in this order:

1. `server.log`: bridge startup, long connection errors, Claude command errors.
2. `audit.jsonl`: `receive_message`, `run_input`, `cardkit_create`, `cardkit_reply`, `cardkit_update`, `card_action`.
3. `mget/*.json`: whether the user-visible card reached Feishu and which button state is visible.
4. Feishu app console: long connection mode and event subscription status.
5. Local `lark-cli auth` state: user identity can send to the target group and revoke its own messages.

Do not paste app secrets or tenant tokens into reports.
