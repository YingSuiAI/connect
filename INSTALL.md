# direxio-connect Install

`direxio-connect` is distributed for Direxio only. The supported bridge target is the Direxio Matrix agent room.

## Recommended

Use `direxio-deployer`. It calls Direxio `agent.matrix_session.create`, writes the Matrix-only config, and installs the local daemon.

## Manual Install

Via npm:

```bash
npm install -g direxio-connent
```

Via Homebrew:

```bash
brew install direxio-connect
```

Download binary from GitHub Releases:

```bash
curl -L -o direxio-connect.tar.gz https://github.com/YingSuiAI/direxio-connect/releases/latest/download/direxio-connect-v1.3.12-linux-amd64.tar.gz
tar xzf direxio-connect.tar.gz
chmod +x direxio-connect-v1.3.12-linux-amd64
sudo mv direxio-connect-v1.3.12-linux-amd64 /usr/local/bin/direxio-connect
```

Build from source:

```bash
git clone https://github.com/YingSuiAI/direxio-connect.git
cd cc-connect
make build AGENTS=codex PLATFORMS_INCLUDE=matrix
```

## Config

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

Start foreground:

```bash
direxio-connect --config /path/to/config.toml
```

## Hermes ACP Adapter

Hermes ACP should be launched through the Direxio compatibility adapter so reasoning text is buffered and cleaned before it reaches the Matrix room:

```toml
[projects.agent]
type = "acp"

[projects.agent.options]
work_dir = "/path/to/project"
cmd = "direxio-connect"
args = ["hermes-acp-adapter", "--", "hermes", "acp"]
display_name = "Hermes ACP"
```

Install daemon:

```bash
direxio-connect daemon install --config /path/to/config.toml --force
```

Install a named daemon for one Direxio node:

```bash
direxio-connect daemon install --config /path/to/t1/config.toml --service-name t1.direxio.ai --force
```

Check daemon:

```bash
direxio-connect daemon status
direxio-connect daemon status --service-name t1.direxio.ai
direxio-connect daemon logs -n 100
```
