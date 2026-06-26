# direxio-connect Development Guide

This repository is the Direxio-maintained fork of cc-connect. It is no longer a general multi-chat-platform bridge.

## Scope

- The runtime bridges one local coding agent to one Direxio Matrix agents room.
- The only supported platform implementation is `platform/matrix`.
- The Matrix account must be the Direxio local `@agent:<server>` identity returned by `agent.matrix_session.create`.
- The bridge must be restricted to the real persisted Direxio `agent_room_id` through `room_id`.
- Do not add back non-Direxio chat integrations.

## Packaging

- npm package: `@direxio/connent`
- primary binary: `direxio-connect`
- GitHub repository and release source: `https://github.com/YingSuiAI/connect`
- Homebrew command documented for operators: `brew install direxio-connect`

## Development

- Keep `type = "matrix"` as the only accepted project platform type.
- Keep the agent runtime code reusable for Codex, Claude Code, Gemini, Cursor, Copilot, and similar local coding agents.
- Build with `make build AGENTS=codex PLATFORMS_INCLUDE=matrix` for a focused local binary.
- Run targeted tests for changed packages and `make build` before reporting completion.
