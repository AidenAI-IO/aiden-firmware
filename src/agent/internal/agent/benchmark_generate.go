package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
)

// formatToolsListForPrompt builds the tools list section of the system prompt.
// Mirrors the C++ logic in config_web.cpp:2879-2923 to keep prompt format identical.
func (s *Server) formatToolsListForPrompt() string {
	if s.runtime == nil || s.runtime.tools == nil {
		return "  - keyboard_tap(keys)\n  - keyboard_text(text)\n  - mouse_click(x, y, button, coord_space)\n  - screenshot()\n"
	}
	names := s.runtime.tools.Names()
	if len(names) == 0 {
		return "  - keyboard_tap(keys)\n  - keyboard_text(text)\n  - mouse_click(x, y, button, coord_space)\n  - screenshot()\n"
	}
	var b strings.Builder
	for i, name := range names {
		if i >= 30 {
			break
		}
		b.WriteString("  - " + name + "\n")
	}
	return b.String()
}

func (s *Server) handleBenchmarkGenerate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.Prompt == "" || body.Name == "" {
		http.Error(w, `{"ok":false,"error":"prompt and name required"}`, http.StatusBadRequest)
		return
	}

	if s.runtime == nil {
		http.Error(w, `{"ok":false,"error":"runtime not configured"}`, http.StatusServiceUnavailable)
		return
	}
	model, err := s.runtime.models.Get()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"ok":false,"error":"model not available: %s"}`, err), http.StatusBadGateway)
		return
	}

	toolsList := s.formatToolsListForPrompt()
	systemPrompt := benchmarkGeneratePromptPart1 + toolsList + benchmarkGeneratePromptPart2
	userMessage := "User scenario: " + body.Prompt + "\n\nSuite name: " + body.Name

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	resp, err := model.GenerateContent(ctx, []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, userMessage),
	}, llms.WithTemperature(0.7), llms.WithMaxTokens(4000))
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}
	if len(resp.Choices) == 0 {
		http.Error(w, `{"ok":false,"error":"empty response"}`, http.StatusBadGateway)
		return
	}
	suiteJSON := strings.TrimSpace(resp.Choices[0].Content)
	suiteJSON = strings.TrimPrefix(suiteJSON, "```json")
	suiteJSON = strings.TrimPrefix(suiteJSON, "```")
	suiteJSON = strings.TrimSuffix(suiteJSON, "```")
	suiteJSON = strings.TrimSpace(suiteJSON)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "suite_json": suiteJSON})
}

type perceptionRequest struct {
	Name          string `json:"name"`
	TaskID        string `json:"task_id"`
	UserIntent    string `json:"user_intent"`
	ScreenshotB64 string `json:"screenshot_b64"`
	TargetBox     struct {
		X1 int `json:"x1"`
		Y1 int `json:"y1"`
		X2 int `json:"x2"`
		Y2 int `json:"y2"`
	} `json:"target_box_normalized"`
	TargetName string `json:"target_name"`
}

func slugify(s string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevUnderscore = false
		case r == ' ' || r == '_' || r == '-':
			if !prevUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.TrimRight(b.String(), "_")
}

func (s *Server) handleBenchmarkGeneratePerception(w http.ResponseWriter, r *http.Request) {
	var req perceptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"ok":false,"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if req.TaskID == "" || req.UserIntent == "" || req.ScreenshotB64 == "" || req.TargetName == "" {
		http.Error(w, `{"ok":false,"error":"missing required fields"}`, http.StatusBadRequest)
		return
	}

	if s.runtime == nil {
		http.Error(w, `{"ok":false,"error":"runtime not configured"}`, http.StatusServiceUnavailable)
		return
	}
	model, err := s.runtime.models.Get()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"ok":false,"error":"model not available: %s"}`, err), http.StatusBadGateway)
		return
	}

	userText := fmt.Sprintf("task_id: %s\nuser_intent: %s\ntarget_name: %s\ntarget rectangle (normalized 0-1000): (%d,%d)-(%d,%d)\n",
		req.TaskID, req.UserIntent, req.TargetName,
		req.TargetBox.X1, req.TargetBox.Y1, req.TargetBox.X2, req.TargetBox.Y2)

	imgURL := "data:image/jpeg;base64," + req.ScreenshotB64
	parts := []llms.ContentPart{
		llms.TextPart(userText),
		llms.ImageURLPart(imgURL),
	}
	msgs := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, benchmarkPerceptionSystemPrompt),
		{Role: llms.ChatMessageTypeHuman, Parts: parts},
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	resp, err := model.GenerateContent(ctx, msgs,
		llms.WithTemperature(0.3), llms.WithMaxTokens(2000))
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error()), http.StatusBadGateway)
		return
	}
	if len(resp.Choices) == 0 {
		http.Error(w, `{"ok":false,"error":"empty response"}`, http.StatusBadGateway)
		return
	}
	raw := strings.TrimSpace(resp.Choices[0].Content)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	xLo := fmt.Sprintf("%d", req.TargetBox.X1)
	xHi := fmt.Sprintf("%d", req.TargetBox.X2)
	yLo := fmt.Sprintf("%d", req.TargetBox.Y1)
	yHi := fmt.Sprintf("%d", req.TargetBox.Y2)
	slug := slugify(req.TargetName)
	raw = strings.ReplaceAll(raw, "PLACEHOLDER_X", xLo+", "+xHi)
	raw = strings.ReplaceAll(raw, "PLACEHOLDER_Y", yLo+", "+yHi)
	raw = strings.ReplaceAll(raw, "<slug>", slug)

	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		http.Error(w, fmt.Sprintf(`{"ok":false,"error":"LLM did not return valid JSON: %s","raw":%q}`, err, raw), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "task_json": raw})
}
