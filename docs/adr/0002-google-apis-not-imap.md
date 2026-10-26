# 2. Talk to Google's APIs, not IMAP

**Status:** Accepted  
**Date:** 2026-10-26

## Context

sandesh needs both Gmail and Google Chat. IMAP and SMTP would cover mail with existing tools (isync, msmtp), but Google Chat has no open protocol: its only programmatic access is the Chat REST API with OAuth. Workspace admins also often turn IMAP off.

## Decision

Use the Gmail API, the Google Chat API and the People API over plain REST, with one OAuth client the user creates in their own Cloud project. Sign-in is the loopback flow with PKCE; refresh tokens go to the Secret Service keyring through `secret-tool`.

## Consequences

- One sign-in covers mail and chat, and Gmail's labels, threads, drafts and search syntax work as they do in Gmail.
- Every user has to create an OAuth client (about five minutes, documented in the README). A shared client would need Google's verification for restricted scopes.
- Chat needs a Workspace account; a personal Gmail gets mail only.
- The clients are thin and hand-written, so tests run against an in-memory fake (`internal/fake`) instead of Google.
