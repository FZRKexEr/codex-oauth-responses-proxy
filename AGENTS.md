# AGENTS.md

This file provides guidance to Codex (Codex.ai/code) when working with code in this repository.

## What This Is

A Go proxy that wraps ChatGPT's OAuth-authenticated `chatgpt.com/backend-api/codex/*` endpoints into standard OpenAI-style APIs (`/v1/responses`, `/v1/chat/completions`, `/v1/models`). Single-user, local-first.

## Build & Run

```bash
make build          # → bin/oauth-responses-proxy
make run            # run with token file at project root
make run-debug      # same but logs request bodies (DEBUG_REQUEST_BODY=true)
make check          # compile check (go build ./...)
make fmt            # gofmt all Go files
```

No test suite exists yet — regression is done manually via curl (see README for the full checklist).

## Architecture

```
main.go                          # wires config → store → auth → proxy → httpapi
internal/
  config/config.go               # env-var-driven config, builds oauth2.Config
  store/store.go                 # mutex-guarded JSON file for tokens + pending device-code auth state
  auth/service.go                # OAuth token refresh; JWT account-ID extraction
  auth/device.go                 # ChatGPT device-code login, polling, authorization-code exchange
  proxy/service.go               # upstream request builder, payload transform, SSE→JSON, usage-limit remapping
  httpapi/handler.go             # routes + handlers for health, auth, models, responses
  httpapi/chat_completions.go    # chat completions → responses translation layer
```

### Request flow for `/v1/responses`

1. `httpapi.handleResponses` validates auth (optional API key) and parses JSON body.
2. `proxy.BuildResponsesRequest` applies mandatory adaptations: forces `stream=true`, `store=false`, auto-fills empty `instructions`, strips `prompt_cache_retention`, `safety_identifier`, and `max_output_tokens`.
3. If client requested streaming: SSE is piped through as-is. If non-streaming: proxy collects the full SSE stream, extracts the `response.done`/`response.completed` event, returns the final JSON.

### Request flow for `/v1/chat/completions`

1. `translateChatCompletionsRequest` converts Chat Completions format (messages, tools, tool_choice, reasoning_effort, response_format) into a Responses API payload.
2. The translated payload goes through the same `proxy.BuildResponsesRequest` path.
3. The upstream Responses result is converted back via `responsesToChatCompletion` (non-stream) or `writeChatCompletionSSE` (stream — reconstructed from final response, not token-by-token).

### Key upstream quirks the proxy works around

- Missing `instructions` → upstream 400; proxy auto-fills `""`.
- `stream=false` → upstream rejects; proxy always sends `stream=true` and reassembles for non-stream callers.
- `prompt_cache_retention`, `safety_identifier`, `max_output_tokens` → upstream rejects; proxy strips them.
- Upstream 404 with usage-limit messages → proxy remaps to 429.

## Code Conventions

- Go standard library for HTTP; no router framework (just `http.NewServeMux`).
- All config via environment variables with sensible defaults (see `config.go`).
- JSON payloads are `map[string]any` throughout — no typed request/response structs.
- Logging uses `log.Printf` with structured-ish key=value format.
- Token refresh happens lazily in `proxy.GetValidTokens()` with a configurable buffer window.
