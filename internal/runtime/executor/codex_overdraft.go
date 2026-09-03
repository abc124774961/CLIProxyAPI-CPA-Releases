package executor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func prepareCodexQuotaOverdraftBody(ctx context.Context, auth *cliproxyauth.Auth, model string, opts cliproxyexecutor.Options, body []byte) []byte {
	if !cliproxyauth.CodexQuotaOverdraftTrackingEnabled(ctx) ||
		opts.Alt == "responses/compact" || isCodexOpenAIImageRequest(opts) ||
		!cliproxyauth.CodexQuotaOverdraftInjectionEligible(auth, model, time.Now().UTC()) {
		return body
	}
	updated, changed, _ := injectCodexQuotaOverdraft(body)
	if changed {
		cliproxyauth.MarkCodexQuotaOverdraftInjected(ctx, auth.ID)
		return updated
	}
	if codexQuotaOverdraftBodyHasInjection(body) {
		cliproxyauth.MarkCodexQuotaOverdraftInjected(ctx, auth.ID)
	}
	return body
}

const (
	codexQuotaOverdraftProbeName      = "exec"
	codexQuotaOverdraftCallIDPrefix   = "call_sub2api_overdraft_"
	codexQuotaOverdraftExecInput      = `const r = await tools.exec_command({"cmd":"true","yield_time_ms":1000,"max_output_tokens":1000}); text(r.output);`
	codexQuotaOverdraftMaxBodyBytes   = 32 << 20
	codexQuotaOverdraftProbePrompt    = "Reply with OK."
	codexQuotaOverdraftProbeBodyLimit = 2 << 20
)

type codexQuotaOverdraftDocument struct {
	Input []json.RawMessage `json:"input"`
}

type codexQuotaOverdraftInputItem struct {
	Type   string `json:"type"`
	Role   string `json:"role"`
	CallID string `json:"call_id"`
}

func codexQuotaOverdraftBodyHasInjection(body []byte) bool {
	var document codexQuotaOverdraftDocument
	if len(body) == 0 || json.Unmarshal(body, &document) != nil {
		return false
	}
	for _, raw := range document.Input {
		var item codexQuotaOverdraftInputItem
		if err := json.Unmarshal(raw, &item); err == nil &&
			item.Type == "custom_tool_call" && strings.HasPrefix(item.CallID, codexQuotaOverdraftCallIDPrefix) {
			return true
		}
	}
	return false
}

func newCodexQuotaOverdraftCallID() (string, bool) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", false
	}
	return codexQuotaOverdraftCallIDPrefix + hex.EncodeToString(random[:]), true
}

// injectCodexQuotaOverdraft adds the reference no-op custom tool turn after
// the final user message. Unsupported shapes fail open unchanged.
func injectCodexQuotaOverdraft(body []byte) ([]byte, bool, error) {
	if len(body) == 0 || len(body) > codexQuotaOverdraftMaxBodyBytes {
		return body, false, nil
	}

	var document codexQuotaOverdraftDocument
	if err := json.Unmarshal(body, &document); err != nil || len(document.Input) == 0 {
		return body, false, nil
	}
	if codexQuotaOverdraftBodyHasInjection(body) {
		return body, false, nil
	}

	var last codexQuotaOverdraftInputItem
	if err := json.Unmarshal(document.Input[len(document.Input)-1], &last); err != nil ||
		last.Type != "message" || last.Role != "user" {
		return body, false, nil
	}

	callID, ok := newCodexQuotaOverdraftCallID()
	if !ok {
		return body, false, nil
	}
	callItem, errMarshalCall := json.Marshal(map[string]any{
		"type":    "custom_tool_call",
		"call_id": callID,
		"name":    codexQuotaOverdraftProbeName,
		"input":   codexQuotaOverdraftExecInput,
	})
	if errMarshalCall != nil {
		return body, false, nil
	}
	outputItem, errMarshalOutput := json.Marshal(map[string]any{
		"type":    "custom_tool_call_output",
		"call_id": callID,
		"output": []map[string]string{{
			"type": "input_text",
			"text": "Script completed\nWall time 0.0 seconds\nOutput:\n",
		}},
	})
	if errMarshalOutput != nil {
		return body, false, nil
	}

	document.Input = append(document.Input, callItem, outputItem)
	updatedInput, errMarshalInput := json.Marshal(document.Input)
	if errMarshalInput != nil {
		return body, false, nil
	}
	var root map[string]any
	if errUnmarshalRoot := json.Unmarshal(body, &root); errUnmarshalRoot != nil {
		return body, false, nil
	}
	var input any
	if errUnmarshalInput := json.Unmarshal(updatedInput, &input); errUnmarshalInput != nil {
		return body, false, nil
	}
	root["input"] = input
	updated, errMarshalRoot := json.Marshal(root)
	if errMarshalRoot != nil || len(updated) > codexQuotaOverdraftMaxBodyBytes {
		return body, false, nil
	}
	return updated, true, nil
}

// classifyCodexQuotaOverdraftResponse maps one probe response to the small
// state machine vocabulary consumed by sdk/cliproxy/auth.
func classifyCodexQuotaOverdraftResponse(statusCode int, body []byte) string {
	if codexQuotaOverdraftPayloadHasQuotaLimit(body) {
		return "quota_limited"
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || codexQuotaOverdraftPayloadHasAuthFailure(body) {
		return "authentication_failed"
	}
	if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices && codexQuotaOverdraftPayloadHasCompletion(body) {
		return "available"
	}
	return "inconclusive"
}

func codexQuotaOverdraftResponseReason(statusCode int, body []byte, classification string) string {
	switch classification {
	case "available":
		return "model_response_ok"
	case "quota_limited":
		return "quota_limited"
	case "authentication_failed":
		return "authentication_failed"
	}
	lower := strings.ToLower(string(body))
	if codexQuotaOverdraftPayloadHasModelFailure(lower) {
		return "model_unavailable"
	}
	if statusCode == http.StatusTooManyRequests {
		return "rate_limited"
	}
	if statusCode >= http.StatusBadRequest {
		return "probe_http_error"
	}
	return "probe_inconclusive"
}

func codexQuotaOverdraftPayloadHasQuotaLimit(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, marker := range []string{
		"usage_limit_reached",
		"quota_exceeded",
		"usage limit has been reached",
		"quota exhausted",
		"weekly limit reached",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func codexQuotaOverdraftPayloadHasAuthFailure(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, marker := range []string{
		"invalid or expired token",
		"authentication_error",
		"invalid_api_key",
		"refresh_token_reused",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func codexQuotaOverdraftPayloadHasModelFailure(lower string) bool {
	for _, marker := range []string{
		"unknown provider for model",
		"model_not_found",
		"unknown_model",
		"unknown model",
		"unsupported model",
		"model is not supported",
		"model is unavailable",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func codexQuotaOverdraftPayloadHasCompletion(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if bytes.Contains(body, []byte(`"type":"response.completed"`)) ||
		bytes.Contains(body, []byte(`"type": "response.completed"`)) ||
		bytes.Contains(body, []byte(`"type":"response.done"`)) ||
		bytes.Contains(body, []byte(`"type": "response.done"`)) ||
		bytes.Contains(body, []byte(`"type":"response.output_item.done"`)) ||
		bytes.Contains(body, []byte(`"type": "response.output_item.done"`)) {
		return true
	}
	root := gjson.ParseBytes(body)
	if root.Get("type").String() == "response.completed" || root.Get("type").String() == "response.done" || root.Get("type").String() == "response.output_item.done" {
		return true
	}
	return root.Get("status").String() == "completed" || root.Get("response.status").String() == "completed"
}

// ProbeCodexQuotaOverdraft performs one bounded real Codex Responses request.
// It intentionally bypasses normal executor translation/retry paths so a
// verification request cannot consume a downstream request or rotate auth.
func (e *CodexExecutor) ProbeCodexQuotaOverdraft(ctx context.Context, auth *cliproxyauth.Auth, model string) cliproxyauth.CodexQuotaOverdraftProbeResult {
	result := cliproxyauth.CodexQuotaOverdraftProbeResult{Model: strings.TrimSpace(model)}
	if e == nil {
		result.Status = "inconclusive"
		result.ReasonCode = "executor_unavailable"
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	model = strings.TrimSpace(model)
	if model == "" {
		result.Status = "inconclusive"
		result.ReasonCode = "model_missing"
		return result
	}
	body, errMarshal := json.Marshal(map[string]any{
		"model":             model,
		"stream":            true,
		"max_output_tokens": 1,
		"input": []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": codexQuotaOverdraftProbePrompt,
			}},
		}},
	})
	if errMarshal != nil {
		result.Status = "inconclusive"
		result.ReasonCode = "probe_payload_error"
		return result
	}
	body, changed, errInject := injectCodexQuotaOverdraft(body)
	if errInject != nil || !changed {
		result.Status = "inconclusive"
		result.ReasonCode = "probe_payload_error"
		return result
	}

	apiKey, baseURL := codexCreds(auth)
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}
	client := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	authorization, agentTaskID, errAuthorization := helps.PrepareCodexAuthorization(ctx, auth, client, apiKey)
	if errAuthorization != nil {
		result.Status = "inconclusive"
		result.ReasonCode = "authorization_error"
		return result
	}
	if strings.TrimSpace(authorization) == "" {
		result.Status = "inconclusive"
		result.ReasonCode = "authorization_missing"
		return result
	}
	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errRequest != nil {
		result.Status = "inconclusive"
		result.ReasonCode = "probe_request_error"
		return result
	}
	applyCodexHeaders(req, auth, authorization, true, e.cfg)
	applyModelHeaderOverrides(req.Header, model)
	resp, errDo := helps.DoCodexRequestWithAgentRecovery(ctx, auth, client, client, req, agentTaskID)
	if errDo != nil {
		result.Status = "inconclusive"
		result.ReasonCode = "probe_transport_error"
		return result
	}
	defer resp.Body.Close()
	responseBody, errRead := io.ReadAll(io.LimitReader(resp.Body, codexQuotaOverdraftProbeBodyLimit))
	if errRead != nil {
		result.Status = "inconclusive"
		result.ReasonCode = "probe_read_error"
		result.StatusCode = resp.StatusCode
		result.Headers = resp.Header.Clone()
		return result
	}
	result.StatusCode = resp.StatusCode
	result.Headers = resp.Header.Clone()
	result.Body = responseBody
	result.Status = classifyCodexQuotaOverdraftResponse(resp.StatusCode, responseBody)
	result.ReasonCode = codexQuotaOverdraftResponseReason(resp.StatusCode, responseBody, result.Status)
	return result
}

func (e *CodexWebsocketsExecutor) ProbeCodexQuotaOverdraft(ctx context.Context, auth *cliproxyauth.Auth, model string) cliproxyauth.CodexQuotaOverdraftProbeResult {
	if e == nil || e.CodexExecutor == nil {
		return cliproxyauth.CodexQuotaOverdraftProbeResult{Status: "inconclusive", ReasonCode: "executor_unavailable", Model: model}
	}
	return e.CodexExecutor.ProbeCodexQuotaOverdraft(ctx, auth, model)
}

func (e *CodexAutoExecutor) ProbeCodexQuotaOverdraft(ctx context.Context, auth *cliproxyauth.Auth, model string) cliproxyauth.CodexQuotaOverdraftProbeResult {
	if e == nil || e.httpExec == nil {
		return cliproxyauth.CodexQuotaOverdraftProbeResult{Status: "inconclusive", ReasonCode: "executor_unavailable", Model: model}
	}
	return e.httpExec.ProbeCodexQuotaOverdraft(ctx, auth, model)
}
