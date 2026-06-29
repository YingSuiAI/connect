# direxio-connect

Direxio 专用 Matrix 桥接器，用于把本地 coding agent 接入当前 Direxio agent room。

这个分支保留 cc-connect 的 agent runtime 和 Matrix transport，删除上游多聊天平台集成。Direxio 部署流程应该为本地 `@agent:<server>` 用户创建 Matrix session，写入 Matrix-only 配置，并让 `direxio-connect` 只监听真实的 `agent_room_id`。

## 安装

npm:

```bash
npm install -g @direxio/connent
```

Homebrew:

```bash
brew install direxio-connect
```

GitHub Releases:

```bash
curl -L -o direxio-connect.tar.gz https://github.com/YingSuiAI/connect/releases/latest/download/direxio-connect-v1.3.11-linux-amd64.tar.gz
tar xzf direxio-connect.tar.gz
chmod +x direxio-connect-v1.3.11-linux-amd64
sudo mv direxio-connect-v1.3.11-linux-amd64 /usr/local/bin/direxio-connect
```

源码构建:

```bash
git clone https://github.com/YingSuiAI/connect.git
cd cc-connect
make build AGENTS=codex PLATFORMS_INCLUDE=matrix
```

## Matrix 配置

`direxio-deployer` 会自动生成配置。手动配置仅用于本地调试。

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

运行:

```bash
direxio-connect --config /path/to/config.toml
```

### Hermes ACP Adapter

Hermes ACP 应通过 Direxio 兼容层启动，这样推理文本会先被缓存和清洗，不会直接进入 Matrix agent room：

```toml
[projects.agent]
type = "acp"

[projects.agent.options]
work_dir = "/path/to/project"
cmd = "direxio-connect"
args = ["hermes-acp-adapter", "--", "hermes", "acp"]
display_name = "Hermes ACP"
```

安装后台服务:

```bash
direxio-connect daemon install --config /path/to/config.toml --force
```

同一台电脑连接多个 Direxio 节点时，每个后台服务使用不同的 service name：

```bash
direxio-connect daemon install --config /path/to/t1/config.toml --service-name t1.direxio.ai --force
direxio-connect daemon status --service-name t1.direxio.ai
```

## Direxio 约束

- Matrix 用户必须是本地 `@agent:<server>`，不能使用 portal owner session。
- `room_id` 必须是真实持久化的 Direxio `agent_room_id`；deployer 会拒绝 `!agent:<domain>` 这类旧伪 id。
- 仅支持 `type = "matrix"`。
- 上游 cc-connect 的其他聊天平台已按 Direxio 需求删除。
