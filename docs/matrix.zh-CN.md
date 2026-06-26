# Direxio Matrix 桥接

`direxio-connect` 只支持 Direxio Matrix agent room。

不要配置公共 Matrix 账号，也不要使用个人 Element access token。Direxio message server 必须通过 `agent.matrix_session.create` 为本地 `@agent:<server>` 用户创建 Matrix Client-Server session。

## 必要配置

`direxio-deployer` 会自动写入配置：

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

## 运行规则

- `room_id` 必填，且必须是真实的 Direxio `agent_room_id`。
- 非 `room_id` 房间的事件会被忽略。
- 回复必须由 Matrix `@agent:<server>` 用户发送。
- 不能使用 portal owner session 发送 agent 回复。
- 配置校验只接受 `type = "matrix"`。
