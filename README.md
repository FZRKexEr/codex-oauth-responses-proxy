# codex-oauth-responses-proxy

把 ChatGPT OAuth 登录后的 `chatgpt.com/backend-api/codex/*` 接口包装成 OpenAI 风格接口的单用户代理。

对外提供：

- `GET /v1/models`
- `POST /v1/responses`
- `POST /v1/chat/completions`

它适合给自己的 coding agent 或客户端使用，不是多租户网关，也不是 OpenAI Platform API 的替代品。

## 快速启动

```bash
make build
make run
```

默认监听：

```text
http://127.0.0.1:1455
```

`make run` 会把 OAuth token 保存到项目根目录：

```text
.oauth_tokens.json
```

公开部署时必须设置代理访问密钥：

```bash
export PROXY_API_KEY="your-secret-key"
export OPENAI_OAUTH_TOKEN_FILE="/opt/codex-oauth-responses-proxy/.oauth_tokens.json"
./bin/oauth-responses-proxy
```

`PROXY_API_KEY` 是访问这个代理服务的密钥，不是 OpenAI key，也不是 ChatGPT OAuth token。

## 登录

项目只支持 device-code 登录，不需要浏览器回调到服务器。

1. 获取设备码：

```bash
curl -s http://127.0.0.1:1455/auth/login \
  -H 'authorization: Bearer your-secret-key' | jq .
```

响应里会有：

```json
{
  "verification_url": "https://auth.openai.com/codex/device",
  "user_code": "ABCD-1234",
  "interval": 5,
  "expires_at": 1770000000
}
```

2. 在浏览器打开 `verification_url`，登录 ChatGPT，并输入 `user_code`
3. 完成登录并保存 token：

```bash
curl -X POST http://127.0.0.1:1455/auth/login/complete \
  -H 'authorization: Bearer your-secret-key' | jq .
```

返回 `{"ok":true}` 即成功。部署在远程服务器时，把示例里的地址换成你的服务域名即可。

检查状态：

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

## 接口

设置 `PROXY_API_KEY` 后，下面接口都需要：

```http
Authorization: Bearer your-secret-key
```

受保护接口：

- `GET /auth/login`
- `POST /auth/login/complete`
- `GET /v1/models`
- `POST /v1/responses`
- `POST /v1/chat/completions`

公开接口：

- `GET /health`

### `POST /v1/responses`

OpenAI Responses 风格入口。代理会转发到 ChatGPT 的 Codex backend，并处理上游的若干兼容差异。

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

兼容旧客户端。内部会把 chat completions 请求转换成 responses 请求，再把结果转回 `chat.completion` 或 `chat.completion.chunk`。

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

返回上游 Codex backend 的模型列表，并整理成 OpenAI `model list` 风格。

```bash
curl -s http://127.0.0.1:1455/v1/models \
  -H 'authorization: Bearer your-secret-key' | jq .
```

## 上游兼容差异

真实上游和公开 Responses API 不完全一致，代理会做最小必要适配：

- 缺少 `instructions` 时自动补 `""`
- 强制上游请求使用 `stream=true`
- 非流式客户端请求会在代理侧收完整 SSE 后返回最终 JSON
- 强制 `store=false`
- 转发前移除上游不支持的 `prompt_cache_retention`
- 转发前移除上游不支持的 `safety_identifier`
- 转发前移除上游不支持的 `max_output_tokens`；因此输出 token 上限不会被上游执行
- 上游用量限制类 404 会改写成 429

当前仍不支持：

- `previous_response_id`
- 图片输入和更复杂的多模态事件重写
- 多租户隔离

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `LISTEN_ADDR` | `127.0.0.1:1455` | 服务监听地址 |
| `OPENAI_OAUTH_TOKEN_FILE` | `.oauth_tokens.json` | OAuth token 落盘位置 |
| `PROXY_API_KEY` | 空 | 代理访问密钥；公开部署必须设置 |
| `OPENAI_BACKEND_BASE` | `https://chatgpt.com/backend-api/codex` | Codex backend 地址 |
| `OPENAI_PROXY_TIMEOUT` | `600s` | 上游请求超时 |
| `OPENAI_OAUTH_REFRESH_BUFFER_SECONDS` | `300` | token 提前刷新窗口 |

一般不需要改的 OAuth 参数：

- `OPENAI_OAUTH_CLIENT_ID`
- `OPENAI_OAUTH_TOKEN_URL`
- `OPENAI_OAUTH_ORIGINATOR`
- `OPENAI_OAUTH_BETA`

## 开发命令

```bash
make fmt
make check
make build
make run-debug
```

当前没有自动化测试；改动代理逻辑后，至少用真实账号手动回归 `/health`、`/v1/models`、`/v1/responses` 和 `/v1/chat/completions`。
