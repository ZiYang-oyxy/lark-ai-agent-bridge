# E2E Environment Profiles

Environment files bind generic E2E roles to one local deployment. Real App
IDs, machine paths, audit paths, and profile names are local configuration and
must not be committed.

1. Copy `profile.example.env` to an ignored file such as `local.env`.
2. Replace every fixture or `/tmp` value with the local test environment.
3. Keep App Secrets, chat IDs, open IDs, and OAuth tokens in
   `~/.lark-agent-bridge/e2e/profiles/<profile-name>.env`; never place them in
   `docs/environments/`.

Run deterministic E2E with `make test-l2 ENV=local`. Run the real-agent canary
with `make test-l3 ENV=local`.
