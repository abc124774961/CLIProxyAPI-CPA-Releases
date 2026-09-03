package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

// classifyFailure derives a category from structured body text, trusted
// response headers, transport errors, and status. It is only called on failure
// paths, so successful requests pay no parsing cost.
func classifyFailure(err *Error, cfg *internalconfig.Config) FailureKind {
	if err == nil {
		return FailureKindUnknownTransient
	}
	if isRequestScopedResultError(err) || clientRequestBody(errorTextForClassification(err)) {
		setErrorFailureKind(err, FailureKindRequestFault)
		return FailureKindRequestFault
	}
	if isCredentialFailureResult(err) {
		kind := FailureKindAuth
		// An explicit authentication signal wins over the transport status. In
		// particular, a provider may incorrectly wrap an invalid token in a
		// 429/502 response; rotating it as a quota or edge failure would hide the
		// account problem and make the cooldown decision inconsistent.
		if !hasExplicitCredentialAuthSignal(err) &&
			(statusCodeFromResult(err) == http.StatusTooManyRequests || isQuotaMessage(errorTextForClassification(err))) {
			kind = FailureKindQuota
		}
		setErrorFailureKind(err, kind)
		return kind
	}
	if statusCodeFromResult(err) == http.StatusTooManyRequests {
		setErrorFailureKind(err, FailureKindQuota)
		return FailureKindQuota
	}
	if isEdgeGatewayError(err) {
		setErrorFailureKind(err, FailureKindEdgeGateway)
		return FailureKindEdgeGateway
	}
	if isCapacityError(err) {
		setErrorFailureKind(err, FailureKindCapacity)
		return FailureKindCapacity
	}
	if isTransportErrorMessage(errorTextForClassification(err)) {
		setErrorFailureKind(err, FailureKindTransport)
		return FailureKindTransport
	}
	if evidence := errorEvidence(err); evidence != nil && evidence.OutputStarted {
		setErrorFailureKind(err, FailureKindStreamQuality)
		return FailureKindStreamQuality
	}
	setErrorFailureKind(err, FailureKindUnknownTransient)
	return FailureKindUnknownTransient
}

func errorTextForClassification(err *Error) string {
	if err == nil {
		return ""
	}
	evidence := errorEvidence(err)
	if evidence == nil || len(evidence.Body) == 0 {
		return err.Message
	}
	if strings.TrimSpace(err.Message) == "" {
		return string(evidence.Body)
	}
	return err.Message + "\n" + string(evidence.Body)
}

func isCredentialFailureResult(err *Error) bool {
	if err == nil {
		return false
	}
	if isTerminalCredentialResultError(err) {
		return true
	}
	status := statusCodeFromResult(err)
	if status == http.StatusUnauthorized || status == http.StatusPaymentRequired {
		return true
	}
	if status == http.StatusForbidden && !isCloudflareChallengeResultError(err) {
		return true
	}
	if evidence := errorEvidence(err); evidence != nil {
		for _, name := range []string{"WWW-Authenticate", "X-Auth-Error", "X-Authentication-Error"} {
			if strings.TrimSpace(responseHeaderValue(evidence.Headers, name)) != "" {
				return true
			}
		}
	}
	lower := strings.ToLower(err.Code + " " + errorTextForClassification(err))
	return strings.Contains(lower, "invalid token") || strings.Contains(lower, "invalid grant") ||
		strings.Contains(lower, "authentication_error") || strings.Contains(lower, "bad-credentials") ||
		strings.Contains(lower, "insufficient balance") || strings.Contains(lower, "balance insufficient")
}

func hasExplicitCredentialAuthSignal(err *Error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromResult(err)
	if status == http.StatusUnauthorized || status == http.StatusPaymentRequired || status == http.StatusForbidden {
		return true
	}
	if evidence := errorEvidence(err); evidence != nil {
		for _, name := range []string{"WWW-Authenticate", "X-Auth-Error", "X-Authentication-Error"} {
			if strings.TrimSpace(responseHeaderValue(evidence.Headers, name)) != "" {
				return true
			}
		}
	}
	lower := strings.ToLower(err.Code + " " + errorTextForClassification(err))
	return strings.Contains(lower, "invalid token") || strings.Contains(lower, "invalid grant") ||
		strings.Contains(lower, "authentication_error") || strings.Contains(lower, "bad-credentials") ||
		strings.Contains(lower, "insufficient balance") || strings.Contains(lower, "balance insufficient")
}

func isQuotaMessage(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "quota") || strings.Contains(lower, "usage_limit") ||
		strings.Contains(lower, "rate limit") || strings.Contains(lower, "rate_limit")
}

func isEdgeGatewayError(err *Error) bool {
	if err == nil || statusCodeFromResult(err) != http.StatusBadGateway {
		return false
	}
	if isCredentialFailureResult(err) || isRequestScopedResultError(err) {
		return false
	}
	if evidence := errorEvidence(err); evidence != nil {
		headers := evidence.Headers
		if responseHeaderValue(headers, "CF-Ray") != "" || responseHeaderValue(headers, "CF-Cache-Status") != "" ||
			strings.Contains(strings.ToLower(responseHeaderValue(headers, "Server")), "cloudflare") {
			return true
		}
	}
	lower := strings.ToLower(errorTextForClassification(err))
	if strings.Contains(lower, "bad gateway") || strings.Contains(lower, "gateway error") ||
		strings.Contains(lower, "upstream connect error") ||
		strings.Contains(lower, "websocket_upstream_disconnected") ||
		strings.Contains(lower, "upstream websocket disconnected") ||
		strings.Contains(lower, "websocket: close 1006") {
		return true
	}
	// A remaining 502 is still an edge/upstream gateway failure. Explicit
	// request and credential signals were handled above, so treating the
	// status itself as sufficient prevents an opaque proxy response from
	// incorrectly cooling or disabling the selected account.
	return true
}

func isCapacityError(err *Error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromResult(err)
	if status != http.StatusServiceUnavailable && status != 529 {
		return false
	}
	// A 503 from an upstream is a service-capacity signal even when the
	// provider returns a terse/empty body.  Keep it out of account lifecycle
	// handling so an overloaded service does not disable an otherwise healthy
	// credential.  529 is the provider-specific overload equivalent.
	return true
}

func (m *Manager) errorHandlingConfig() internalconfig.ErrorHandlingConfig {
	// Temporary network handling is a code-owned policy. Keep the behavior
	// consistent across config reloads and management clients; legacy
	// error-handling YAML values are ignored by the config loader. A zero-value
	// snapshot is kept for embedders that construct Manager with a partial Config
	// and still rely on the pre-policy cooldown behavior.
	if m != nil {
		if cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config); cfg != nil && cfg.ErrorHandling == (internalconfig.ErrorHandlingConfig{}) {
			defaults := internalconfig.DefaultErrorHandlingConfig()
			defaults.TransientErrorsKeepAccountActive = false
			return defaults
		}
	}
	return internalconfig.DefaultErrorHandlingConfig()
}

func (m *Manager) keepsTransientAccountActive(err *Error) bool {
	if err == nil {
		return false
	}
	kind := errorFailureKind(err)
	if kind == "" {
		kind = classifyFailure(err, nil)
	}
	switch kind {
	case FailureKindEdgeGateway, FailureKindCapacity, FailureKindTransport, FailureKindStreamQuality:
		return m.errorHandlingConfig().TransientErrorsKeepAccountActive
	default:
		return false
	}
}

func isTemporaryFailureKind(kind FailureKind) bool {
	switch kind {
	case FailureKindEdgeGateway, FailureKindCapacity, FailureKindTransport,
		FailureKindStreamQuality, FailureKindUnknownTransient:
		return true
	default:
		return false
	}
}

func isTemporaryFailureResult(err *Error) bool {
	if err == nil || isModelSupportResultError(err) || isRequestScopedResultError(err) {
		return false
	}
	return isTemporaryFailureKind(temporaryFailureKind(err))
}

func temporaryFailureKind(err *Error) FailureKind {
	if err == nil {
		return FailureKindUnknownTransient
	}
	if errorFailureKind(err) == "" {
		return classifyFailure(err, nil)
	}
	return errorFailureKind(err)
}

// transientRetryAction applies the request-local temporary failure policy.
// It returns whether another credential may be attempted and performs the
// optional bounded Retry-After wait. A temporary failure is deliberately
// limited to one account switch per request.
func (m *Manager) transientRetryAction(ctx context.Context, err *Error, switches *int) (bool, error) {
	if err == nil || !isTemporaryFailureResult(err) {
		return true, nil
	}
	cfg := m.errorHandlingConfig()
	if evidence := errorEvidence(err); cfg.RetryBeforeFirstOutputOnly && evidence != nil && evidence.OutputStarted {
		m.errorHandlingMetrics.retriesBlockedAfterOutput.Add(1)
		return false, nil
	}
	if cfg.TemporaryErrorStrategy == internalconfig.TemporaryErrorNoSwitch {
		return false, nil
	}
	if switches != nil && *switches >= 1 {
		return false, nil
	}
	if switches != nil {
		(*switches)++
		m.errorHandlingMetrics.retrySwitches.Add(1)
	}
	if cfg.TemporaryErrorStrategy == internalconfig.TemporaryErrorWaitThenSwitch {
		maxWait := time.Duration(cfg.TemporaryErrorMaxWaitSeconds) * time.Second
		if wait := temporaryErrorWait(err, maxWait); wait > 0 {
			waitStarted := time.Now()
			if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
				return false, errWait
			}
			m.errorHandlingMetrics.retryWaitMilliseconds.Add(uint64(time.Since(waitStarted).Milliseconds()))
		}
	}
	return true, nil
}

func temporaryErrorWait(err *Error, maxWait time.Duration) time.Duration {
	if err == nil || maxWait <= 0 {
		return 0
	}
	if retryAfter := retryAfterFromError(err); retryAfter != nil && *retryAfter > 0 {
		if *retryAfter < maxWait {
			return *retryAfter
		}
		return maxWait
	}
	if evidence := errorEvidence(err); evidence != nil {
		if raw := strings.TrimSpace(responseHeaderValue(evidence.Headers, "Retry-After")); raw != "" {
			if seconds, parseErr := strconv.Atoi(raw); parseErr == nil && seconds > 0 {
				wait := time.Duration(seconds) * time.Second
				if wait < maxWait {
					return wait
				}
				return maxWait
			}
			if when, parseErr := http.ParseTime(raw); parseErr == nil {
				wait := time.Until(when)
				if wait > 0 && wait < maxWait {
					return wait
				}
				if wait >= maxWait {
					return maxWait
				}
			}
		}
	}
	return 0
}

// responseHeaderValue reads a response header case-insensitively. HTTP clients
// normally canonicalize header names, but adapters and tests may construct a
// raw http.Header map with provider-specific casing.
func responseHeaderValue(headers http.Header, name string) string {
	if len(headers) == 0 {
		return ""
	}
	if value := headers.Get(name); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) || len(values) == 0 {
			continue
		}
		return values[0]
	}
	return ""
}

func isTransportErrorMessage(message string) bool {
	lower := strings.ToLower(message)
	// Keep this list deliberately specific: these messages are emitted by the
	// HTTP/TCP/WebSocket stack rather than by an upstream account or request
	// validator. Treating them as transport failures lets the temporary-error
	// policy rotate without putting a healthy credential into cooldown.
	for _, marker := range []string{
		"unexpected eof",
		"connection reset",
		"connection refused",
		"network is unreachable",
		"no route to host",
		"broken pipe",
		"use of closed network connection",
		"i/o timeout",
		"tls handshake timeout",
		"timeout awaiting response headers",
		"stream reset",
		"http2: stream closed",
		"temporary failure in name resolution",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.Contains(lower, "context canceled") || strings.Contains(lower, "context deadline exceeded") || lower == "eof"
}

func clientRequestBody(message string) bool {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return false
	}
	if gjson.Valid(trimmed) {
		for _, path := range []string{"error.code", "code", "error.type", "type"} {
			value := strings.ToLower(strings.TrimSpace(gjson.Get(trimmed, path).String()))
			if value == "invalid_encrypted_content" || value == "cyber_policy" ||
				value == "context_length_exceeded" || value == "invalid_request_error" ||
				value == "previous_response_not_found" || value == "invalid_request" {
				return true
			}
		}
	}
	lower := strings.ToLower(trimmed)
	for _, phrase := range []string{"missing required parameter", "invalid type for input content", "invalid_encrypted_content", "response item not found", "cyber_policy", "context length exceeded"} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func enrichErrorEvidence(err *Error, source error) *Error {
	if err == nil || source == nil {
		return err
	}
	var headers http.Header
	if carrier, ok := source.(interface{ Headers() http.Header }); ok {
		headers = carrier.Headers()
	} else {
		var nested interface{ Headers() http.Header }
		if errors.As(source, &nested) && nested != nil {
			headers = nested.Headers()
		}
	}
	if headers != nil {
		evidence := errorEvidence(err)
		if evidence == nil {
			evidence = &FailureEvidence{}
		}
		evidence.Headers = headers
		setErrorEvidence(err, evidence)
	}
	if carrier, ok := source.(interface{ ResponseHeaders() http.Header }); ok {
		evidence := errorEvidence(err)
		if evidence == nil {
			evidence = &FailureEvidence{}
		}
		evidence.Headers = carrier.ResponseHeaders()
		setErrorEvidence(err, evidence)
	} else {
		var nested interface{ ResponseHeaders() http.Header }
		if errors.As(source, &nested) && nested != nil {
			evidence := errorEvidence(err)
			if evidence == nil {
				evidence = &FailureEvidence{}
			}
			evidence.Headers = nested.ResponseHeaders()
			setErrorEvidence(err, evidence)
		}
	}
	var body []byte
	if carrier, ok := source.(interface{ ResponseBody() []byte }); ok {
		body = carrier.ResponseBody()
	} else {
		var nested interface{ ResponseBody() []byte }
		if errors.As(source, &nested) && nested != nil {
			body = nested.ResponseBody()
		}
	}
	if len(body) > 0 {
		if len(body) > maxFailureEvidenceBodyBytes {
			body = body[:maxFailureEvidenceBodyBytes]
		}
		evidence := errorEvidence(err)
		if evidence == nil {
			evidence = &FailureEvidence{}
		}
		// Copy the bounded slice because provider error implementations are
		// allowed to reuse their response buffer after returning the error.
		// Classification evidence is request-local and must remain stable while
		// hooks, cooldown bookkeeping, and retry selection inspect it.
		evidence.Body = append([]byte(nil), body...)
		setErrorEvidence(err, evidence)
		if len(err.Message) == 0 {
			err.Message = string(evidence.Body)
		}
	}
	if errors.Is(source, context.Canceled) || errors.Is(source, context.DeadlineExceeded) || errors.Is(source, io.EOF) || errors.Is(source, io.ErrUnexpectedEOF) {
		if errorEvidence(err) == nil {
			setErrorEvidence(err, &FailureEvidence{})
		}
	}
	return err
}
