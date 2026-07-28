#!/usr/bin/env bash
# smoke: preflight (utility case, dispatched by name only, not in a tier array)

case_preflight() {
  ./scripts/e2e-preflight.sh >"$RUN_DIR/preflight.log" 2>&1
  summary "- preflight: passed"
}
