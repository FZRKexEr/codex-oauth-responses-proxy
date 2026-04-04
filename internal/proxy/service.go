package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"oauth-responses-proxy/internal/auth"
	"oauth-responses-proxy/internal/config"
	"oauth-responses-proxy/internal/store"
)

type Service struct {
	cfg        config.Config
	store      *store.TokenStore
	httpClient *http.Client
	auth       *auth.Service
}

func NewService(cfg config.Config, store *store.TokenStore, httpClient *http.Client, authService *auth.Service) *Service {
	return &Service{cfg: cfg, store: store, httpClient: httpClient, auth: authService}
}

func (s *Service) GetValidTokens() (*store.Tokens, error) {
	tokens, err := s.store.LoadTokens()
	if err != nil {
		return nil, err
	}
	if tokens == nil {
		log.Printf("proxy: no stored tokens available")
		return nil, errors.New("not authenticated; visit /auth/login first")
	}
	if time.Now().Unix() < tokens.ExpiresAt-int64(s.cfg.RefreshBuffer.Seconds()) {
		log.Printf("proxy: using cached access token account_id=%s", tokens.AccountID)
		return tokens, nil
	}
	log.Printf("proxy: access token nearing expiry; refreshing account_id=%s", tokens.AccountID)
	refreshed, err := s.auth.RefreshTokens(tokens.RefreshToken)
	if err != nil {
		_ = s.store.ClearTokens()
		log.Printf("proxy: cleared stored tokens after refresh failure: %v", err)
		return nil, err
	}
	if err := s.store.SaveTokens(refreshed); err != nil {
		return nil, err
	}
	log.Printf("proxy: saved refreshed access token account_id=%s", refreshed.AccountID)
	return refreshed, nil
}

func (s *Service) FetchModels(ctx context.Context, tokens *store.Tokens) ([]byte, int, error) {
	log.Printf("proxy: fetching models from upstream")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.BackendBase+"/codex/models?client_version=1.0.0", nil)
	if err != nil {
		return nil, 0, err
	}
	addBackendHeaders(req.Header, s.cfg, tokens, false)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("proxy: upstream models status=%d bytes=%d", resp.StatusCode, len(body))
	return body, resp.StatusCode, nil
}

func TransformResponsesPayload(payload map[string]any) (map[string]any, bool) {
	body := cloneMap(payload)
	requestedStream, _ := body["stream"].(bool)
	_, droppedPromptCacheRetention := body["prompt_cache_retention"]
	delete(body, "prompt_cache_retention")
	_, droppedSafetyIdentifier := body["safety_identifier"]
	delete(body, "safety_identifier")
	body["store"] = false
	body["stream"] = true
	if _, exists := body["instructions"]; !exists {
		body["instructions"] = ""
	}
	model, _ := body["model"].(string)
	_, hasInstructions := payload["instructions"]
	log.Printf(
		"proxy: transformed responses payload model=%s requested_stream=%t forced_stream=true instructions_auto=%t prompt_cache_retention_dropped=%t safety_identifier_dropped=%t input_items=%d tools=%d keys=%v",
		model,
		requestedStream,
		!hasInstructions,
		droppedPromptCacheRetention,
		droppedSafetyIdentifier,
		arrayLen(body["input"]),
		arrayLen(body["tools"]),
		mapKeys(body),
	)
	return body, requestedStream
}

func (s *Service) BuildResponsesRequest(ctx context.Context, payload map[string]any, tokens *store.Tokens) (*http.Request, bool, error) {
	upstreamPayload, requestedStream := TransformResponsesPayload(payload)
	body, err := json.Marshal(upstreamPayload)
	if err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.BackendBase+"/codex/responses", bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	addBackendHeaders(req.Header, s.cfg, tokens, requestedStream)
	req.Header.Set("Content-Type", "application/json")
	log.Printf("proxy: built upstream responses request requested_stream=%t accept_sse=%t", requestedStream, requestedStream)
	if s.cfg.DebugRequestBody {
		log.Printf("proxy: upstream request body=%s", string(body))
	}
	return req, requestedStream, nil
}

func (s *Service) Do(req *http.Request) (*http.Response, error) {
	resp, err := s.httpClient.Do(req)
	if err != nil {
		log.Printf("proxy: upstream request failed: %v", err)
		return nil, err
	}
	log.Printf("proxy: upstream request completed status=%d path=%s", resp.StatusCode, req.URL.Path)
	return resp, nil
}

func SSEToFinalJSON(sse string) (map[string]any, error) {
	for _, line := range strings.Split(sse, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		eventType, _ := event["type"].(string)
		if eventType == "response.done" || eventType == "response.completed" {
			response, ok := event["response"].(map[string]any)
			if ok {
				return response, nil
			}
		}
	}
	return nil, errors.New("could not find final response in upstream SSE stream")
}

func MapUsageLimit404(status int, body string) int {
	if status != http.StatusNotFound {
		return status
	}
	haystack := strings.ToLower(body)
	for _, marker := range []string{"usage_limit_reached", "usage_not_included", "rate_limit_exceeded", "usage limit"} {
		if strings.Contains(haystack, marker) {
			return http.StatusTooManyRequests
		}
	}
	return status
}

func addBackendHeaders(header http.Header, cfg config.Config, tokens *store.Tokens, acceptSSE bool) {
	header.Set("Authorization", "Bearer "+tokens.AccessToken)
	header.Set("chatgpt-account-id", tokens.AccountID)
	header.Set("originator", cfg.Originator)
	header.Set("OpenAI-Beta", cfg.BetaHeader)
	if acceptSSE {
		header.Set("Accept", "text/event-stream")
	}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func arrayLen(value any) int {
	items, ok := value.([]any)
	if !ok {
		return 0
	}
	return len(items)
}

func mapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
