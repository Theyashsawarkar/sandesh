# 4. One Go binary

**Status:** Accepted  
**Date:** 2026-10-26

## Context

Most of the family is bash or Python with no dependencies. sandesh needs OAuth, a long-running sync, SQLite with full-text search, an MCP server and a full-screen TUI with an editor round trip.

## Decision

Write sandesh in Go as one static binary: Bubble Tea and Lip Gloss for the screen, modernc.org/sqlite (pure Go, FTS5), golang.org/x/oauth2, and the official MCP Go SDK. The screen, CLI, MCP server and daemon are subcommands of the same binary.

## Consequences

- Install is one file (release binaries for amd64 and arm64), no runtime to manage.
- Building from source needs Go; the tmux plugin builds its own copy on first use when `sandesh` isn't on PATH.
- Front ends share one core package, so behaviour can't drift between them.
