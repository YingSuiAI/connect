# direxio-connect

Direxio-only Matrix bridge for connecting a local coding agent to the current Direxio agent room.

This fork keeps the cc-connect agent runtime and Matrix transport, and removes the upstream multi-platform chat integrations. Direxio deployment should create a Matrix session for the local `@agent:<server>` user, write a Matrix-only config, and run `direxio-connect` against the real `agent_room_id`.

## Install

Via npm:

```bash
npm install -g @direxio/connent
```

Via Homebrew:

```bash
brew install direxio-connect
```

From GitHub Releases:

```bash
curl -L -o direxio-connect.tar.gz https://github.com/YingSuiAI/connect/releases/latest/download/direxio-connect-v1.3.4-linux-amd64.tar.gz
tar xzf direxio-connect.tar.gz
chmod +x direxio-connect
sudo mv direxio-connect /usr/local/bin/direxio-connect
```

Build from source:

```bash
git clone https://github.com/YingSuiAI/connect.git
cd cc-connect
make build AGENTS=codex PLATFORMS_INCLUDE=matrix
```

## Matrix Config

`direxio-deployer` should generate this file automatically. Manual config is only for local debugging.

```toml
[[projects]]
name = "direxio-agent-room"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/path/to/project"

[[projects.platforms]]
type = "matrix"

[projects.platforms.options]
homeserver = "http://127.0.0.1:8008"
access_token = "agent-matrix-access-token"
user_id = "@agent:example.com"
device_id = "DIREXIO_CC_CONNECT"
room_id = "!real-agent-room:example.com"
share_session_in_channel = true
group_reply_all = true
auto_join = false
auto_verify = false
```

Run:

```bash
direxio-connect --config /path/to/config.toml
```

Install as a daemon:

```bash
direxio-connect daemon install --config /path/to/config.toml --force
```

## Direxio Requirements

- The Matrix user must be the local `@agent:<server>` identity, not the portal owner.
- `room_id` must be the real persisted Direxio `agent_room_id`; pseudo ids such as `!agent:<domain>` are rejected by deployer wiring.
- Only `type = "matrix"` is supported.
- Other chat platforms from upstream cc-connect are intentionally removed.
