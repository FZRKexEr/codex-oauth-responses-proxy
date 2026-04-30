# codex-oauth-responses-proxy

一个基于 ChatGPT OAuth 的本地 Go 代理，对外暴露 OpenAI 风格的：

- `POST /v1/responses`
- `POST /v1/chat/completions`
- `GET /v1/models`

GitHub 仓库：

- [FZRKexEr/codex-oauth-responses-proxy](https://github.com/FZRKexEr/codex-oauth-responses-proxy)

它的目标不是实现一套新的聊天协议，而是把这条真实上游：

- `https://chatgpt.com/backend-api/codex/responses`

包装成更接近标准 `Responses API` 的本地服务。

## 项目目标

这个项目现在主要提供两层接口：

- 原生 `/v1/responses`
- 面向旧 SDK / 旧 agent / 旧客户端 的 `/v1/chat/completions` 兼容层

明确不做：

- 多租户网关
- 官方 Platform API 的替代品

## 为什么需要代理

真实上游已经通过实测确认有几条行为和公开 `Responses API` 使用习惯不完全一致：

- 缺少 `instructions` 时会失败
- 非流式请求直接打上游会失败，上游要求 `stream=true`
- `previous_response_id` 当前不支持
- `prompt_cache_retention` 当前不支持
- `safety_identifier` 当前不支持
- `max_output_tokens` 当前不支持

所以这个代理做的是“最小必要适配”：

- 强制 `store: false`
- 缺少 `instructions` 时自动补空字符串 `""`
- 对非流式请求，内部转成上游流式，再把 SSE 收口成最终 JSON
- 转发前过滤已确认不兼容的 `prompt_cache_retention`
- 转发前过滤已确认不兼容的 `safety_identifier`
- 转发前过滤已确认不兼容的 `max_output_tokens`
- 对流式请求，原样 SSE passthrough

除了这些已经被真实上游证明必要的适配，其他字段尽量透传。

另外项目现在也提供 `/v1/chat/completions` 兼容层：

- 把 chat completions 请求转换成 responses 请求
- 复用现有 `/v1/responses` 适配逻辑
- 再把结果转换回 `chat.completion` / `chat.completion.chunk`

## 当前结构

代码已经按职责拆开：

- 入口装配： [main.go](/Users/xinpeng/Desktop/Agent/OAuth/main.go)
- 配置： [config.go](/Users/xinpeng/Desktop/Agent/OAuth/internal/config/config.go)
- token 持久化： [store.go](/Users/xinpeng/Desktop/Agent/OAuth/internal/store/store.go)
- OAuth device-code 登录/刷新： [service.go](/Users/xinpeng/Desktop/Agent/OAuth/internal/auth/service.go)
- 上游请求适配： [service.go](/Users/xinpeng/Desktop/Agent/OAuth/internal/proxy/service.go)
- HTTP 接口： [handler.go](/Users/xinpeng/Desktop/Agent/OAuth/internal/httpapi/handler.go)
- chat completions 兼容层： [chat_completions.go](/Users/xinpeng/Desktop/Agent/OAuth/internal/httpapi/chat_completions.go)

## 依赖

主要依赖：

- `golang.org/x/oauth2`
- `github.com/golang-jwt/jwt/v5`

其余 HTTP 服务和转发逻辑使用 Go 标准库。

## 启动

构建：

```bash
make build
```

构建检查：

```bash
make check
```

运行服务：

```bash
make run
```

`make run` 默认会把 token 文件固定到项目根目录：

```text
OPENAI_OAUTH_TOKEN_FILE=<project-root>/.oauth_tokens.json
```

构建产物默认输出到：

```text
bin/oauth-responses-proxy
```

默认监听地址：

```bash
http://127.0.0.1:1455
```

如果你准备把它部署到服务器上，建议同时设置一个服务端 API key：

```bash
PROXY_API_KEY=your-secret-key
```

设置后，这些接口会要求客户端携带：

```http
Authorization: Bearer your-secret-key
```

受保护接口：

- `/v1/models`
- `/v1/responses`
- `/v1/chat/completions`

无需 `PROXY_API_KEY` 的接口：

- `/health`

OAuth 登录相关接口也需要这个 key，避免外部用户重新绑定账号：

- `/auth/login`
- `/auth/login/complete`

## 服务器最小部署配置

如果你只是想把它部署成一个“给自己或自己的 coding agent 使用”的私有服务，最小建议配置就是：

```bash
export PROXY_API_KEY="your-secret-key"
export OPENAI_OAUTH_TOKEN_FILE="/opt/codex-oauth-responses-proxy/.oauth_tokens.json"
./bin/oauth-responses-proxy
```

其中：

- `PROXY_API_KEY` 用来验证客户端是否可以调用你的代理
- `OPENAI_OAUTH_TOKEN_FILE` 用来指定 ChatGPT OAuth token 的落盘位置

其他环境变量大多数都可以先使用默认值。

第一次部署后，你仍然需要完成一次 device-code 登录流程，生成 `.oauth_tokens.json`，之后代理才能代表你的 ChatGPT 账户访问上游。

## 登录

项目只保留 device-code 登录，不再依赖 `localhost` 浏览器回调。

1. 获取设备码：

```bash
curl -s http://127.0.0.1:1455/auth/login \
  -H 'authorization: Bearer your-secret-key' | jq .
```

响应里会包含：

```json
{
  "verification_url": "https://auth.openai.com/codex/device",
  "user_code": "ABCD-1234",
  "interval": 5,
  "expires_at": 1770000000
}
```

2. 用浏览器打开 `verification_url`，登录 ChatGPT，并输入 `user_code`
3. 回到终端完成登录：

```bash
curl -X POST http://127.0.0.1:1455/auth/login/complete \
  -H 'authorization: Bearer your-secret-key' | jq .
```

返回 `{"ok":true}` 即登录成功。远程服务器部署时流程一样，只需要把示例里的地址换成你的服务地址；浏览器不需要回调到代理所在机器。

如果服务端返回 device-code 未启用，需要先在 ChatGPT 个人安全设置或工作区权限里开启 device-code 登录。

### 检查登录状态

```bash
curl -s http://127.0.0.1:1455/health | jq .
```

预期：

- `"ok": true`
- `"authenticated": true`

### Token 文件

默认情况下，`make run` 会把 token 保存在项目根目录的 `.oauth_tokens.json`。

如果手动运行二进制，建议显式指定绝对路径：

```bash
OPENAI_OAUTH_TOKEN_FILE=/absolute/path/to/.oauth_tokens.json ./bin/oauth-responses-proxy
```

## API

### `GET /health`

返回本地服务状态与当前是否已登录。

### `GET /auth/login`

发起 device-code 登录，返回 `verification_url`、`user_code`、轮询间隔和过期时间。设置 `PROXY_API_KEY` 时需要 Bearer 认证。

### `POST /auth/login/complete`

轮询 device-code 登录结果，成功后交换并保存 OAuth tokens。设置 `PROXY_API_KEY` 时需要 Bearer 认证。

### `GET /v1/models`

从真实上游 `backend-api/codex/models` 拉取模型列表，并转成 `model list` 风格响应。

兼容策略：

- 保留上游模型对象里已有字段
- 补标准风格的 `id` / `object`
- 如果上游没有 `created`，默认补 `0`
- 不再硬编码自定义 `owned_by`

### `POST /v1/responses`

对外暴露标准风格 `Responses API` 入口。

### `POST /v1/chat/completions`

对外暴露兼容 OpenAI `chat.completions` 的入口，内部会转换到 `/v1/responses` 再转回 chat completions 响应格式。

## 使用示例

### 非流式文本

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/responses \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "Reply with exactly: NONSTREAM_OK"}
        ]
      }
    ]
  }' | jq .
```

### 流式文本

```bash
curl --noproxy '*' -sN http://127.0.0.1:1455/v1/responses \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "stream": true,
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "Reply with exactly: STREAM_OK"}
        ]
      }
    ]
  }'
```

### Tool call

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/responses \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "Use the tool to get the weather for Paris. Do not answer from memory."}
        ]
      }
    ],
    "tools": [
      {
        "type": "function",
        "name": "get_weather",
        "description": "Get current weather for a city.",
        "parameters": {
          "type": "object",
          "properties": {
            "city": {"type": "string"}
          },
          "required": ["city"],
          "additionalProperties": false
        }
      }
    ]
  }' | jq .
```

### Chat completions 非流式

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/chat/completions \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "messages": [
      {"role": "system", "content": "Reply briefly."},
      {"role": "user", "content": "Reply with exactly: CHAT_OK"}
    ]
  }' | jq .
```

### Chat completions 流式

```bash
curl --noproxy '*' -sN http://127.0.0.1:1455/v1/chat/completions \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "stream": true,
    "messages": [
      {"role": "user", "content": "Reply with exactly: CHAT_STREAM_OK"}
    ]
  }'
```

### Reasoning

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/responses \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "reasoning": {"effort": "high", "summary": "auto"},
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "Reply with exactly: REASONING_OK"}
        ]
      }
    ]
  }' | jq .
```

### Structured output

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/responses \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "instructions": "Return data matching the requested schema.",
    "text": {
      "format": {
        "type": "json_schema",
        "name": "answer",
        "schema": {
          "type": "object",
          "properties": {
            "ok": {"type": "boolean"},
            "value": {"type": "string"}
          },
          "required": ["ok", "value"],
          "additionalProperties": false
        },
        "strict": true
      }
    },
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "Return ok=true and value=JSON_OK"}
        ]
      }
    ]
  }' | jq .
```

## 已通过真实上游验证的能力

以下能力已经直接对真实 `chatgpt.com/backend-api/codex/responses` 测过：

- `GET /v1/models`
- 非流式文本响应
- 流式文本响应
- `tools`
- tool output 回注
- `reasoning`
- `text.format: json_schema`

## 当前 chat completions 兼容层能力

下面这些能力是当前项目内置的 `/v1/chat/completions` 兼容范围：

- `messages` 角色：`system`、`developer`、`user`、`assistant`、`tool`
- `tools`
- `tool_choice`
- assistant `tool_calls`
- tool message 回注
- 非流式 `chat.completion`
- 流式 `chat.completion.chunk`
- `response_format.type=json_schema`
- `max_tokens` / `max_completion_tokens`
- `reasoning_effort -> reasoning.effort`
- 透传 `reasoning`

这层兼容主要是为了让只支持 chat completions 的客户端和 coding agent 更容易接入。

如果客户端本身已经支持 `Responses API`，仍然建议优先直接走 `/v1/responses`。

## 已确认的上游行为差异

这些不是猜测，是已经测出来的结果：

1. `instructions`

- 缺少 `instructions` 时，上游会返回：

```text
Instructions are required
```

- 但 `instructions: ""` 可以通过
- 所以代理会在缺省时自动补空字符串

2. 非流式

- 非流式请求直接打上游会返回：

```text
Stream must be set to true
```

- 所以代理会在内部把非流式请求改成上游流式，再从 SSE 中提取最终 `response.completed`

3. `previous_response_id`

- 当前会返回：

```text
Unsupported parameter: previous_response_id
```

4. `prompt_cache_retention`

- 当前会返回：

```text
Unsupported parameter: prompt_cache_retention
```

- 所以代理会在转发前移除这个字段

5. `safety_identifier`

- 当前会返回：

```text
Unsupported parameter: safety_identifier
```

- 所以代理会在转发前移除这个字段

6. `max_output_tokens`

- 当前会返回：

```text
Unsupported parameter: max_output_tokens
```

- 所以代理会在转发前移除这个字段

## 当前边界

- 主要面向单用户、本地代理场景
- token 默认明文保存在本地文件
- 还没有做图片输入和更复杂的多模态事件重写
- 还没有做自动化测试，只整理了真实手工回归清单
- 还没有做详细的请求日志、监控和压测
- chat completions 的流式输出目前是根据最终 responses 结果重组出来的 chunk，不是逐 token 原样转发

## 最小真实回归测试清单

每次改动代理逻辑后，至少回归下面这些请求。

### 1. 健康检查

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/health | jq .
```

预期：

- 服务可访问
- 已登录时 `authenticated=true`
- 如果配置了 `PROXY_API_KEY`，则 `api_key_required=true`

### 2. 模型列表

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/models \
  -H 'authorization: Bearer your-secret-key' | jq '.data | map(.id)[:10]'
```

预期：

- 返回真实模型列表

### 3. 非流式文本

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/responses \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "Reply with exactly: NONSTREAM_OK"}
        ]
      }
    ]
  }' | jq '{status, text: .output[0].content[0].text}'
```

预期：

- `status=completed`
- 返回 `NONSTREAM_OK`

### 4. 流式文本

```bash
curl --noproxy '*' -sN http://127.0.0.1:1455/v1/responses \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "stream": true,
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "Reply with exactly: STREAM_OK"}
        ]
      }
    ]
  }' | rg 'response\.output_text\.done|response\.completed'
```

预期：

- 能看到 `response.output_text.done`
- 能看到 `response.completed`

### 5. Tool call

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/responses \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "Use the tool to get the weather for Paris. Do not answer from memory."}
        ]
      }
    ],
    "tools": [
      {
        "type": "function",
        "name": "get_weather",
        "description": "Get current weather for a city.",
        "parameters": {
          "type": "object",
          "properties": {"city": {"type": "string"}},
          "required": ["city"],
          "additionalProperties": false
        }
      }
    ]
  }' | jq '.output'
```

预期：

- 返回 `type=function_call`

### 6. Tool output 回注

预期：

- 把 `function_call_output` 回传后，模型能输出最终文本答案

### 7. Reasoning

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/responses \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "reasoning": {"effort": "high", "summary": "auto"},
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "Reply with exactly: REASONING_OK"}
        ]
      }
    ]
  }' | jq '{reasoning, usage, output}'
```

预期：

- `reasoning.effort=high`
- `usage.output_tokens_details.reasoning_tokens > 0`

### 8. Structured output

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/responses \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "instructions": "Return data matching the requested schema.",
    "text": {
      "format": {
        "type": "json_schema",
        "name": "answer",
        "schema": {
          "type": "object",
          "properties": {
            "ok": {"type": "boolean"},
            "value": {"type": "string"}
          },
          "required": ["ok", "value"],
          "additionalProperties": false
        },
        "strict": true
      }
    },
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "Return ok=true and value=JSON_OK"}
        ]
      }
    ]
  }' | jq '{status, text: .output[0].content[0].text}'
```

预期：

- 返回符合 schema 的 JSON 字符串

### 9. 已知不支持项

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/responses \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "previous_response_id": "resp_xxx",
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "Hello"}
        ]
      }
    ]
  }' | jq .
```

预期：

- 返回 `Unsupported parameter: previous_response_id`

如果是直接调用真实上游而不是通过这个代理，下面两个字段目前也会报错：

- `prompt_cache_retention`
- `safety_identifier`
- `max_output_tokens`

而通过本项目转发时，这些字段会被自动过滤，不需要调用方自己处理。

### 10. Chat completions 兼容检查

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/chat/completions \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "messages": [
      {"role": "system", "content": "Reply briefly."},
      {"role": "user", "content": "Reply with exactly: CHAT_OK"}
    ]
  }' | jq '{object, model, text: .choices[0].message.content}'
```

预期：

- `object=chat.completion`
- `choices[0].message.content=CHAT_OK`

### 11. Chat completions 推理强度

```bash
curl --noproxy '*' -s http://127.0.0.1:1455/v1/chat/completions \
  -H 'authorization: Bearer your-secret-key' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.3-codex",
    "reasoning_effort": "high",
    "messages": [
      {"role": "user", "content": "Reply with exactly: CHAT_REASONING_OK"}
    ]
  }' | jq '{model, usage, text: .choices[0].message.content}'
```

兼容策略：

- `chat.completions` 的 `reasoning_effort` 会映射到 `responses` 的 `reasoning.effort`
- 如果请求里已经直接带了 `reasoning` 对象，也会继续透传

## 环境变量

- `LISTEN_ADDR`
- `OPENAI_OAUTH_CLIENT_ID`
- `OPENAI_OAUTH_TOKEN_URL`
- `OPENAI_OAUTH_ORIGINATOR`
- `OPENAI_OAUTH_BETA`
- `OPENAI_BACKEND_BASE`
- `OPENAI_OAUTH_TOKEN_FILE`
- `PROXY_API_KEY`
- `OPENAI_PROXY_TIMEOUT`
- `OPENAI_OAUTH_REFRESH_BUFFER_SECONDS`

## 本地忽略文件

项目已通过 [.gitignore](/Users/xinpeng/Desktop/Agent/OAuth/.gitignore) 忽略这些本地产物：

- `.oauth_tokens.json`
- 本地构建二进制
- IDE 文件
- 旧 Python 缓存
