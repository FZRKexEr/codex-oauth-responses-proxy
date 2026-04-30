# codex-oauth-responses-proxy

一个把 ChatGPT/Codex 登录态变成 OpenAI-compatible API 的单用户服务。

客户端可以把它当成 OpenAI endpoint 使用：

```text
Base URL: http://127.0.0.1:1455/v1
API Key:  你的 PROXY_API_KEY
Model:    gpt-5.5
```

对外提供：

- `GET /v1/models`
- `POST /v1/responses`
- `POST /v1/chat/completions`

适合自己或自己的 agent 使用；不建议直接做多人共享网关。

## 启动

```bash
make build
make run
```

默认监听：

```text
http://127.0.0.1:1455
```

`make run` 会把登录 token 保存到项目根目录：

```text
.oauth_tokens.json
```

部署到服务器时建议显式设置：

```bash
export PROXY_API_KEY="your-secret-key"
export OPENAI_OAUTH_TOKEN_FILE="/opt/codex-oauth-responses-proxy/.oauth_tokens.json"
./bin/oauth-responses-proxy
```

`PROXY_API_KEY` 是访问这个代理服务的密钥，不是 OpenAI API key，也不是 ChatGPT 登录 token。

## 登录

支持 device-code 登录。

1. 获取登录码：

```bash
curl -s http://127.0.0.1:1455/auth/login \
  -H 'authorization: Bearer your-secret-key' | jq .
```

返回示例：

```json
{
  "verification_url": "https://auth.openai.com/codex/device",
  "user_code": "ABCD-1234",
  "interval": 5,
  "expires_at": 1770000000
}
```

2. 在浏览器打开 `verification_url`，登录 ChatGPT，并输入 `user_code`
3. 回到终端完成登录：

```bash
curl -X POST http://127.0.0.1:1455/auth/login/complete \
  -H 'authorization: Bearer your-secret-key' | jq .
```

返回 `{"ok":true}` 即成功。部署在远程服务器时，把示例地址换成你的服务域名即可。

检查登录状态：

```bash
curl -s http://127.0.0.1:1455/health | jq .
```

已登录时会看到：

```json
{
  "ok": true,
  "authenticated": true
}
```

## 鉴权

设置 `PROXY_API_KEY` 后，客户端请求需要带：

```http
Authorization: Bearer your-secret-key
```

需要鉴权的接口：

- `GET /auth/login`
- `POST /auth/login`
- `POST /auth/login/complete`
- `GET /v1/models`
- `POST /v1/responses`
- `POST /v1/chat/completions`

不需要鉴权的接口：

- `GET /health`

## API

### `POST /v1/responses`

Responses API 风格入口。

```bash
curl -s http://127.0.0.1:1455/v1/responses \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.5",
    "input": "Reply with exactly: OK"
  }' | jq .
```

### `POST /v1/chat/completions`

给只支持 Chat Completions 的客户端使用。

```bash
curl -s http://127.0.0.1:1455/v1/chat/completions \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.5",
    "messages": [
      {"role": "user", "content": "Reply with exactly: OK"}
    ]
  }' | jq .
```

### `GET /v1/models`

返回可用模型列表。

```bash
curl -s http://127.0.0.1:1455/v1/models \
  -H 'authorization: Bearer your-secret-key' | jq .
```

## 行为说明

- 支持流式和非流式请求
- 支持 `tools`、`tool_choice`、`reasoning`、`text.format`
- `prompt_cache_retention` 会被忽略
- `safety_identifier` 会被忽略
- `max_output_tokens` 会被忽略
- Chat Completions 的 `max_tokens` / `max_completion_tokens` 也不会限制输出长度
- `previous_response_id` 当前不可用
- 图片输入和更复杂的多模态事件暂未处理

如果客户端强依赖输出 token 硬上限，需要在客户端侧自行截断或控制提示词。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `LISTEN_ADDR` | `127.0.0.1:1455` | 服务监听地址 |
| `OPENAI_OAUTH_TOKEN_FILE` | `.oauth_tokens.json` | 登录 token 落盘位置 |
| `PROXY_API_KEY` | 空 | 代理访问密钥；公开部署必须设置 |
| `OPENAI_BACKEND_BASE` | `https://chatgpt.com/backend-api` | ChatGPT backend 地址 |
| `OPENAI_PROXY_TIMEOUT` | `180` | 普通请求超时，单位秒 |
| `OPENAI_OAUTH_REFRESH_BUFFER_SECONDS` | `300` | token 提前刷新窗口，单位秒 |
| `DEBUG_REQUEST_BODY` | `false` | 是否打印请求体，调试时使用 |

一般不需要改的 OAuth 参数：

- `OPENAI_OAUTH_CLIENT_ID`
- `OPENAI_OAUTH_TOKEN_URL`
- `OPENAI_OAUTH_ORIGINATOR`
- `OPENAI_OAUTH_BETA`

## 开发

```bash
make fmt
make check
make build
make run-debug
```

当前没有自动化测试。改动代理逻辑后，至少用真实账号手动回归 `/health`、`/v1/models`、`/v1/responses` 和 `/v1/chat/completions`。
