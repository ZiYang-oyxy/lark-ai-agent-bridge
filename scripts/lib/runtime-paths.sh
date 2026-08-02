#!/usr/bin/env bash

lab_cache_home="${XDG_CACHE_HOME:-${HOME:?HOME is required}/.cache}"
export GOCACHE="${GOCACHE:-$lab_cache_home/lark-ai-agent-bridge/go-build}"
mkdir -p "$GOCACHE"
chmod 700 "$GOCACHE"
