package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"oauth-responses-proxy/internal/auth"
	"oauth-responses-proxy/internal/config"
	"oauth-responses-proxy/internal/proxy"
	"oauth-responses-proxy/internal/store"
)

type Handler struct {
	cfg   config.Config
	store *store.TokenStore
	auth  *auth.Service
	proxy *proxy.Service
}

func NewHandler(cfg config.Config, store *store.TokenStore, authService *auth.Service, proxyService *proxy.Service) *Handler {
	return &Handler{cfg: cfg, store: store, auth: authService, proxy: proxyService}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.handleHealth)
	mux.HandleFunc("/auth/login", h.handleAuthLogin)
	mux.HandleFunc("/auth/device/start", h.handleAuthLogin)
	mux.HandleFunc("/auth/device/complete", h.handleAuthDeviceComplete)
	mux.HandleFunc("/v1/models", h.requireAPIKey(h.handleModels))
	mux.HandleFunc("/v1/chat/completions", h.requireAPIKey(h.handleChatCompletions))
	mux.HandleFunc("/v1/responses", h.requireAPIKey(h.handleResponses))
	return mux
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	log.Printf("http: %s %s", r.Method, r.URL.Path)
	tokens, err := h.store.LoadTokens()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pendingDevice, err := h.store.LoadPendingDevice()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"authenticated":       tokens != nil,
		"device_auth_pending": pendingDevice != nil,
		"token_file":          h.cfg.TokenFile,
		"api_key_required":    strings.TrimSpace(h.cfg.ProxyAPIKey) != "",
	})
}

func (h *Handler) requireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	if strings.TrimSpace(h.cfg.ProxyAPIKey) == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !validBearerToken(r.Header.Get("Authorization"), h.cfg.ProxyAPIKey) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="codex-oauth-responses-proxy"`)
			writeError(w, http.StatusUnauthorized, "invalid or missing api key")
			return
		}
		next(w, r)
	}
}

func validBearerToken(headerValue, expected string) bool {
	headerValue = strings.TrimSpace(headerValue)
	expected = strings.TrimSpace(expected)
	if headerValue == "" || expected == "" {
		return false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(headerValue, prefix) {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(headerValue, prefix)) == expected
}

func (h *Handler) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	log.Printf("http: %s %s", r.Method, r.URL.Path)
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	device, pending, err := h.auth.NewDeviceCode(r.Context())
	if err != nil {
		log.Printf("http: device auth start failed: %v", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := h.store.SavePendingDevice(pending); err != nil {
		log.Printf("http: failed to save pending device auth: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("http: device auth pending expires_at=%d", device.ExpiresAt)
	writeJSON(w, http.StatusOK, map[string]any{
		"verification_url": device.VerificationURL,
		"user_code":        device.UserCode,
		"interval":         device.Interval,
		"expires_at":       device.ExpiresAt,
		"message":          "Open verification_url in your browser, enter user_code, then call /auth/device/complete.",
	})
}

func (h *Handler) handleAuthDeviceComplete(w http.ResponseWriter, r *http.Request) {
	log.Printf("http: %s %s", r.Method, r.URL.Path)
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	pending, err := h.store.LoadPendingDevice()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pending == nil {
		writeError(w, http.StatusBadRequest, "no pending device auth flow; start with /auth/login")
		return
	}
	tokens, err := h.auth.ExchangeDeviceCode(r.Context(), pending)
	if err != nil {
		log.Printf("http: device auth complete failed: %v", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := h.store.SaveTokens(tokens); err != nil {
		log.Printf("http: failed to save device auth tokens: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.store.ClearPendingDevice()
	log.Printf("http: device auth completed account_id=%s", tokens.AccountID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account_id": tokens.AccountID})
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	log.Printf("http: %s %s", r.Method, r.URL.Path)
	tokens, err := h.proxy.GetValidTokens()
	if err != nil {
		log.Printf("http: models auth failed: %v", err)
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	body, status, err := h.proxy.FetchModels(r.Context(), tokens)
	if err != nil {
		log.Printf("http: models upstream failed: %v", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if status >= 400 {
		log.Printf("http: models upstream returned status=%d", status)
		writeRawError(w, status, body)
		return
	}
	var upstream map[string]any
	if err := json.Unmarshal(body, &upstream); err != nil {
		writeError(w, http.StatusBadGateway, "invalid upstream json")
		return
	}
	var data []map[string]any
	models, _ := upstream["models"].([]any)
	for _, item := range models {
		model, ok := item.(map[string]any)
		if !ok {
			continue
		}
		slug, _ := model["slug"].(string)
		if slug == "" {
			continue
		}
		entry := make(map[string]any, len(model)+2)
		for key, value := range model {
			entry[key] = value
		}
		entry["id"] = slug
		entry["object"] = "model"
		if _, exists := entry["created"]; !exists {
			entry["created"] = 0
		}
		delete(entry, "slug")
		data = append(data, entry)
	}
	log.Printf("http: models returned count=%d", len(data))
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (h *Handler) handleResponses(w http.ResponseWriter, r *http.Request) {
	log.Printf("http: %s %s", r.Method, r.URL.Path)
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tokens, err := h.proxy.GetValidTokens()
	if err != nil {
		log.Printf("http: responses auth failed: %v", err)
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	req, requestedStream, err := h.proxy.BuildResponsesRequest(r.Context(), payload, tokens)
	if err != nil {
		log.Printf("http: failed to build upstream responses request: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	model, _ := payload["model"].(string)
	log.Printf("http: responses request model=%s requested_stream=%t", model, requestedStream)

	if requestedStream {
		streamClient := &http.Client{Timeout: 0}
		resp, err := streamClient.Do(req)
		if err != nil {
			log.Printf("http: streaming upstream request failed: %v", err)
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			errorBody, _ := io.ReadAll(resp.Body)
			log.Printf("http: streaming upstream returned status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(errorBody)))
			writeRawError(w, proxy.MapUsageLimit404(resp.StatusCode, string(errorBody)), errorBody)
			return
		}
		log.Printf("http: streaming upstream connected status=%d", resp.StatusCode)
		w.Header().Set("Content-Type", headerOrDefault(resp.Header.Get("Content-Type"), "text/event-stream; charset=utf-8"))
		w.WriteHeader(resp.StatusCode)
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 32*1024)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				if _, err := w.Write(buf[:n]); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					log.Printf("http: streaming response completed")
					return
				}
				log.Printf("http: streaming response ended with read error: %v", readErr)
				return
			}
		}
	}

	resp, err := h.proxy.Do(req)
	if err != nil {
		log.Printf("http: non-stream upstream request failed: %v", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		log.Printf("http: non-stream upstream returned status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		writeRawError(w, proxy.MapUsageLimit404(resp.StatusCode, string(respBody)), respBody)
		return
	}
	finalResponse, err := proxy.SSEToFinalJSON(string(respBody))
	if err != nil {
		log.Printf("http: failed to extract final response from SSE: %v", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	log.Printf("http: non-stream response completed")
	writeJSON(w, http.StatusOK, finalResponse)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": detail}})
}

func writeRawError(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func headerOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
