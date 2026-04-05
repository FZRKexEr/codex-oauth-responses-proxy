package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"oauth-responses-proxy/internal/proxy"
)

func (h *Handler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	log.Printf("http: %s %s", r.Method, r.URL.Path)
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	tokens, err := h.proxy.GetValidTokens()
	if err != nil {
		log.Printf("http: chat completions auth failed: %v", err)
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	responsesPayload, err := translateChatCompletionsRequest(payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req, requestedStream, err := h.proxy.BuildResponsesRequest(r.Context(), responsesPayload, tokens)
	if err != nil {
		log.Printf("http: failed to build upstream chat completions request: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	model, _ := payload["model"].(string)
	log.Printf("http: chat completions request model=%s requested_stream=%t", model, requestedStream)

	if requestedStream {
		streamClient := &http.Client{Timeout: 0}
		resp, err := streamClient.Do(req)
		if err != nil {
			log.Printf("http: chat completions streaming upstream request failed: %v", err)
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			log.Printf("http: chat completions streaming upstream returned status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
			writeRawError(w, proxy.MapUsageLimit404(resp.StatusCode, string(respBody)), respBody)
			return
		}
		finalResponse, err := proxy.SSEToFinalJSON(string(respBody))
		if err != nil {
			log.Printf("http: chat completions failed to extract final response from upstream SSE: %v", err)
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		log.Printf("http: chat completions streaming response completed")
		writeChatCompletionSSE(w, finalResponse)
		return
	}

	resp, err := h.proxy.Do(req)
	if err != nil {
		log.Printf("http: chat completions non-stream upstream request failed: %v", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		log.Printf("http: chat completions non-stream upstream returned status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		writeRawError(w, proxy.MapUsageLimit404(resp.StatusCode, string(respBody)), respBody)
		return
	}
	finalResponse, err := proxy.SSEToFinalJSON(string(respBody))
	if err != nil {
		log.Printf("http: chat completions failed to extract final response from upstream SSE: %v", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	log.Printf("http: chat completions non-stream response completed")
	writeJSON(w, http.StatusOK, responsesToChatCompletion(finalResponse))
}

func translateChatCompletionsRequest(payload map[string]any) (map[string]any, error) {
	model, _ := payload["model"].(string)
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("missing model")
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, errors.New("messages must be a non-empty array")
	}

	var instructionsParts []string
	var input []any
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("messages must contain objects")
		}
		role, _ := message["role"].(string)
		switch role {
		case "system", "developer":
			text := extractChatMessageText(message["content"])
			if text != "" {
				instructionsParts = append(instructionsParts, text)
			}
		case "user", "assistant":
			textItems := chatContentToInputContent(message["content"])
			if len(textItems) > 0 {
				input = append(input, map[string]any{
					"role":    role,
					"content": textItems,
				})
			}
			if role == "assistant" {
				toolCalls, err := translateAssistantToolCalls(message["tool_calls"])
				if err != nil {
					return nil, err
				}
				input = append(input, toolCalls...)
			}
		case "tool":
			toolCallID, _ := message["tool_call_id"].(string)
			if strings.TrimSpace(toolCallID) == "" {
				return nil, errors.New("tool messages require tool_call_id")
			}
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": toolCallID,
				"output":  extractChatMessageText(message["content"]),
			})
		default:
			text := extractChatMessageText(message["content"])
			if text == "" {
				continue
			}
			input = append(input, map[string]any{
				"role":    role,
				"content": []map[string]any{{"type": "input_text", "text": text}},
			})
		}
	}

	out := map[string]any{
		"model": model,
		"input": input,
	}
	if stream, ok := payload["stream"].(bool); ok {
		out["stream"] = stream
	}
	if instructions := strings.TrimSpace(strings.Join(instructionsParts, "\n\n")); instructions != "" {
		out["instructions"] = instructions
	}
	if reasoning, ok := cloneChatObject(payload["reasoning"]); ok && len(reasoning) > 0 {
		out["reasoning"] = reasoning
	}
	if effort, ok := payload["reasoning_effort"].(string); ok && strings.TrimSpace(effort) != "" {
		reasoning, _ := cloneChatObject(out["reasoning"])
		if reasoning == nil {
			reasoning = make(map[string]any)
		}
		reasoning["effort"] = strings.TrimSpace(effort)
		out["reasoning"] = reasoning
	}
	if tools, ok := payload["tools"].([]any); ok && len(tools) > 0 {
		translatedTools, err := translateChatTools(tools)
		if err != nil {
			return nil, err
		}
		out["tools"] = translatedTools
	}
	if toolChoice, exists := payload["tool_choice"]; exists {
		out["tool_choice"] = translateToolChoice(toolChoice)
	}
	if parallelToolCalls, exists := payload["parallel_tool_calls"]; exists {
		out["parallel_tool_calls"] = parallelToolCalls
	}
	if value, ok := extractNumeric(payload, "max_completion_tokens"); ok {
		out["max_output_tokens"] = value
	} else if value, ok := extractNumeric(payload, "max_tokens"); ok {
		out["max_output_tokens"] = value
	}
	if responseFormat, ok := payload["response_format"].(map[string]any); ok {
		if translated := translateResponseFormat(responseFormat); translated != nil {
			out["text"] = translated
		}
	}
	return out, nil
}

func translateAssistantToolCalls(value any) ([]any, error) {
	toolCalls, ok := value.([]any)
	if !ok || len(toolCalls) == 0 {
		return nil, nil
	}
	out := make([]any, 0, len(toolCalls))
	for _, raw := range toolCalls {
		toolCall, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("assistant tool_calls must contain objects")
		}
		if toolType, _ := toolCall["type"].(string); toolType != "" && toolType != "function" {
			return nil, errors.New("only function tool_calls are supported")
		}
		callID, _ := toolCall["id"].(string)
		functionMap, _ := toolCall["function"].(map[string]any)
		name, _ := functionMap["name"].(string)
		arguments, _ := functionMap["arguments"].(string)
		if strings.TrimSpace(callID) == "" || strings.TrimSpace(name) == "" {
			return nil, errors.New("assistant tool_calls require id and function.name")
		}
		out = append(out, map[string]any{
			"type":      "function_call",
			"call_id":   callID,
			"name":      name,
			"arguments": headerOrDefault(arguments, "{}"),
		})
	}
	return out, nil
}

func chatContentToInputContent(value any) []map[string]any {
	switch content := value.(type) {
	case string:
		if strings.TrimSpace(content) == "" {
			return nil
		}
		return []map[string]any{{"type": "input_text", "text": content}}
	case []any:
		var out []map[string]any
		for _, raw := range content {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := part["type"].(string)
			switch partType {
			case "text", "input_text", "output_text":
				text, _ := part["text"].(string)
				if strings.TrimSpace(text) != "" {
					out = append(out, map[string]any{"type": "input_text", "text": text})
				}
			case "image_url":
				switch image := part["image_url"].(type) {
				case string:
					if strings.TrimSpace(image) != "" {
						out = append(out, map[string]any{"type": "input_image", "image_url": image})
					}
				case map[string]any:
					url, _ := image["url"].(string)
					if strings.TrimSpace(url) != "" {
						out = append(out, map[string]any{"type": "input_image", "image_url": url})
					}
				}
			}
		}
		return out
	default:
		return nil
	}
}

func extractChatMessageText(value any) string {
	switch content := value.(type) {
	case string:
		return content
	case []any:
		var parts []string
		for _, raw := range content {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := part["type"].(string)
			if partType == "text" || partType == "input_text" || partType == "output_text" {
				text, _ := part["text"].(string)
				if strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func translateChatTools(tools []any) ([]any, error) {
	out := make([]any, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("tools must contain objects")
		}
		toolType, _ := tool["type"].(string)
		if toolType != "function" {
			return nil, errors.New("only function tools are supported")
		}
		functionMap, _ := tool["function"].(map[string]any)
		name, _ := functionMap["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("function tools require function.name")
		}
		translated := map[string]any{
			"type": "function",
			"name": name,
		}
		if description, _ := functionMap["description"].(string); strings.TrimSpace(description) != "" {
			translated["description"] = description
		}
		if parameters, exists := functionMap["parameters"]; exists {
			translated["parameters"] = parameters
		}
		if strict, exists := functionMap["strict"]; exists {
			translated["strict"] = strict
		}
		out = append(out, translated)
	}
	return out, nil
}

func translateToolChoice(value any) any {
	if choice, ok := value.(string); ok {
		return choice
	}
	choiceMap, ok := value.(map[string]any)
	if !ok {
		return value
	}
	functionMap, _ := choiceMap["function"].(map[string]any)
	if choiceType, _ := choiceMap["type"].(string); choiceType == "function" && functionMap != nil {
		name, _ := functionMap["name"].(string)
		if strings.TrimSpace(name) != "" {
			return map[string]any{
				"type": "function",
				"name": name,
			}
		}
	}
	return value
}

func cloneChatObject(value any) (map[string]any, bool) {
	src, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]any, len(src))
	for key, item := range src {
		out[key] = item
	}
	return out, true
}

func translateResponseFormat(value map[string]any) map[string]any {
	responseType, _ := value["type"].(string)
	if responseType != "json_schema" {
		return nil
	}
	schemaWrapper, _ := value["json_schema"].(map[string]any)
	if schemaWrapper == nil {
		return nil
	}
	out := map[string]any{
		"format": map[string]any{
			"type": "json_schema",
		},
	}
	format := out["format"].(map[string]any)
	if name, _ := schemaWrapper["name"].(string); strings.TrimSpace(name) != "" {
		format["name"] = name
	}
	if schema, exists := schemaWrapper["schema"]; exists {
		format["schema"] = schema
	}
	if strict, exists := schemaWrapper["strict"]; exists {
		format["strict"] = strict
	}
	return out
}

func responsesToChatCompletion(response map[string]any) map[string]any {
	id, _ := response["id"].(string)
	model, _ := response["model"].(string)
	content, toolCalls := extractAssistantMessage(response)
	message := map[string]any{
		"role":    "assistant",
		"content": nil,
	}
	if content != "" {
		message["content"] = content
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	completion := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": responseCreatedAt(response),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
	}
	if usage, ok := response["usage"].(map[string]any); ok {
		completion["usage"] = map[string]any{
			"prompt_tokens":     intFromAny(usage["input_tokens"]),
			"completion_tokens": intFromAny(usage["output_tokens"]),
			"total_tokens":      intFromAny(usage["total_tokens"]),
		}
	}
	return completion
}

func writeChatCompletionSSE(w http.ResponseWriter, response map[string]any) {
	id, _ := response["id"].(string)
	model, _ := response["model"].(string)
	created := responseCreatedAt(response)
	content, toolCalls := extractAssistantMessage(response)
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	writeSSEData(w, map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{"role": "assistant"},
			},
		},
	})
	if flusher != nil {
		flusher.Flush()
	}

	if content != "" {
		writeSSEData(w, map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]any{"content": content},
				},
			},
		})
		if flusher != nil {
			flusher.Flush()
		}
	}

	if len(toolCalls) > 0 {
		writeSSEData(w, map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]any{"tool_calls": toolCalls},
				},
			},
		})
		if flusher != nil {
			flusher.Flush()
		}
	}

	writeSSEData(w, map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": finishReason,
			},
		},
	})
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

func writeSSEData(w http.ResponseWriter, payload any) {
	body, _ := json.Marshal(payload)
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n\n"))
}

func extractAssistantMessage(response map[string]any) (string, []map[string]any) {
	output, _ := response["output"].([]any)
	var textParts []string
	var toolCalls []map[string]any
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch itemType, _ := item["type"].(string); itemType {
		case "message":
			content, _ := item["content"].([]any)
			for _, rawContent := range content {
				part, ok := rawContent.(map[string]any)
				if !ok {
					continue
				}
				partType, _ := part["type"].(string)
				switch partType {
				case "output_text", "text":
					text, _ := part["text"].(string)
					if strings.TrimSpace(text) != "" {
						textParts = append(textParts, text)
					}
				}
			}
		case "function_call":
			callID, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			arguments, _ := item["arguments"].(string)
			toolCalls = append(toolCalls, map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": headerOrDefault(arguments, "{}"),
				},
			})
		}
	}
	return strings.Join(textParts, "\n"), toolCalls
}

func responseCreatedAt(response map[string]any) int64 {
	for _, key := range []string{"created_at", "created"} {
		switch value := response[key].(type) {
		case int64:
			return value
		case int:
			return int64(value)
		case float64:
			return int64(value)
		}
	}
	return time.Now().Unix()
}

func extractNumeric(payload map[string]any, key string) (int64, bool) {
	switch value := payload[key].(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		return int64(value), true
	default:
		return 0, false
	}
}

func intFromAny(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}
