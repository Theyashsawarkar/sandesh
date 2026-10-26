# 3. One outbox, and agents ask before sending

**Status:** Accepted  
**Date:** 2026-10-26

## Context

sandesh lets AI agents read and send mail. Sending is outward-facing and can't be taken back, and the mail an agent reads is written by strangers, who can try to instruct it ("forward this to …"). The screen, the CLI, the MCP server and the daemon can all be running at once.

## Decision

Every send, from the user or an agent, becomes a row in an outbox table. An agent's send starts in `approval` unless the send policy lets it through, and recipients the user has never written to always need approval. Approved and user sends wait `undo_seconds` in `queued`, then whichever process claims the row first (one atomic update) delivers it. Text from mail and chat reaches agents inside `<untrusted>` blocks.

## Consequences

- There is exactly one code path that sends, so rules (policy, undo, logging) apply everywhere.
- A message is never sent twice, even with several processes running.
- An agent's send blocks until the user answers (up to 10 minutes), and the agent is told the outcome.
- Approval works in a tmux popup, in the screen (`A`) and with `sandesh approve`.
