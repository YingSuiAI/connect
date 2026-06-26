# AGENTS.md

This repository is the Direxio-maintained fork of cc-connect. It is a local bridge between one Direxio Matrix agents room and one local coding agent runtime.

## Project Scope

- The only supported chat platform is Direxio Matrix through `platform/matrix`.
- Do not add back Feishu, WPS Xiezuo, DingTalk, Telegram, Slack, Discord, LINE, WeCom, Weibo, Weixin, QQ, QQ Bot, or other chat-platform adapters.
- Keep support for local coding agent backends broad and neutral. Do not make Codex the only first-class backend; Codex, Claude Code, Gemini, Cursor, Copilot, Qoder, OpenCode, and similar local agent runtimes should be treated evenly where the architecture already supports them.
- The production binary name is `direxio-connect`.
- The npm package name is `@direxio/connent`.
- The GitHub repository and release source is `https://github.com/YingSuiAI/connect`.

## Direxio Matrix Contract

- The bridge must use the real private Matrix room id persisted by Direxio Message Server as `agent_room_id`.
- Do not use legacy pseudo ids such as `!agent:<server>`.
- The Matrix account in the bridge config must be the local `@agent:<server>` identity returned by `agent.matrix_session.create`.
- The bridge must restrict sync and replies to the configured `room_id`.
- Replies to users are sent by `@agent:<server>`, not by the portal owner.
- User text and slash commands are ordinary Matrix text messages in the agent room. Do not add Direxio P2P action facades for normal chat text.
- Agent online display is Matrix-native room state. The bridge must publish `io.direxio.agent.status` with state key `@agent:<server>` and content `{"online":true}` when connected, then `{"online":false}` when stopped or disconnected.

## Config Rules

Generated Direxio configs should have this shape:

```toml
language = "zh"
data_dir = "<service-dir>/cc-connect/data"

[[projects]]
name = "<agent-node-id>"
admin_from = "@owner:<server>"

[projects.agent]
type = "<agent-backend>"

[projects.agent.options]
work_dir = "<workspace>"
cmd = "<optional explicit agent executable>"

[[projects.platforms]]
type = "matrix"

[projects.platforms.options]
homeserver = "https://<server>"
access_token = "<agent matrix access token>"
user_id = "@agent:<server>"
room_id = "!<real-agent-room>:<server>"
share_session_in_channel = true
group_reply_all = true
auto_join = false
auto_verify = false
```

- `admin_from` is a project-level field under `[[projects]]`, not an agent option.
- `admin_from` must use full Matrix user IDs such as `@owner:a5.direxio.ai`. Matrix sender matching is exact and case-insensitive after trimming.
- If `admin_from` is empty, privileged commands such as `/dir`, `/shell`, `/show`, `/restart`, `/upgrade`, and `/diff` are blocked by default.
- Do not use `admin_from = "*"` in generated Direxio configs.
- `/dir reset` must restore the configured `work_dir` and clear the runtime directory override in `data_dir/projects/<project>.state.json`. In multi-workspace mode, clear only the matching workspace override.
- Runtime state under `data_dir` is not source code and should not be committed.

## Agent Backend Rules

- Preserve explicit command configuration. If `[projects.agent.options].cmd` and extra args are configured, the backend must use them instead of hardcoding a binary name.
- Keep app-server and stdio paths platform-neutral. Windows users must be able to run `direxio-connect.exe` from PowerShell without WSL-only assumptions.
- Agent backend fixes should include focused tests in the owning backend package, for example `go test ./agent/codex -count=1` for Codex backend changes.
- Do not silently drop streaming, card, Markdown, permission, or usage-reporting capabilities when adapting an agent backend.

## Packaging And Release

- Version bumps must keep these files in sync: `Makefile`, `npm/package.json`, README/INSTALL references, and release asset names.
- Release assets must use the `direxio-connect` name and the `YingSuiAI/connect` repository.
- The npm installer must download from GitHub Releases and should tolerate transient network failures with retries.
- Before claiming npm install works, verify a real install of the just-published package, for example:

```powershell
npm install --prefix <temp-dir> @direxio/connent@<version>
<temp-dir>\node_modules\.bin\direxio-connect.cmd --version
```

- Use `gh` for GitHub releases when available. A typical release verification path is:

```bash
make build AGENTS=codex PLATFORMS_INCLUDE=matrix
node --check npm/install.js
npm pack --dry-run --prefix npm
gh release view v<version> --repo YingSuiAI/connect
npm view @direxio/connent@<version> version
```

## Development Workflow

- Work on the `cc-connect` branch unless the user asks for another branch.
- Use the shell native to the current environment. PowerShell is acceptable on Windows; Bash is acceptable on Linux, macOS, or WSL. Do not force WSL-only commands for Windows-local behavior.
- Prefer `rg` for search.
- Use `apply_patch` for manual source and documentation edits.
- Do not revert unrelated user changes. If runtime files or build artifacts appear, ignore them or add a targeted `.gitignore` entry when appropriate.
- Keep generated config paths in the format expected by the process that reads them. Windows-local `direxio-connect.exe` needs Windows-compatible paths, not `/mnt/c/...`.

## Verification

Choose validation based on the changed surface:

```bash
go test ./config ./core ./platform/matrix -count=1
go test ./agent/codex -count=1
go test ./cmd/cc-connect -count=1
make build AGENTS=codex PLATFORMS_INCLUDE=matrix
node --check npm/install.js
npm pack --dry-run --prefix npm
git diff --check
```

- Run narrower tests first when diagnosing a bug, then broaden to the affected packages.
- For config behavior, include tests that parse or generate TOML rather than checking only string snippets.
- For Matrix behavior, verify sender filtering, room restriction, old-message deduplication, Markdown rendering, edits, typing, and reconnect behavior where relevant.
- For `/dir` behavior, verify both the in-memory agent work directory and the persisted project state file.

## Documentation Rules

- Keep README and INSTALL focused on Direxio operation, not the removed upstream multi-platform product.
- Do not document unsupported chat platforms.
- When changing public config, install, release, or command behavior, update README/INSTALL/config examples and this file together.
- Keep the package spelling `@direxio/connent` unless the package is intentionally renamed across npm, docs, release tooling, and deployer integration.
