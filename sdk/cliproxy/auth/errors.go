package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	requestScopedErrorCode              = "request_scoped"
	terminalCredentialErrorCode         = "credential_invalidated"
	transientCredentialContextErrorCode = "transient_credential_context"
)

// connectionLifecycleErrorCode marks transport/session lifecycle failures that
// must skip credential cooldown without being treated as request-scoped faults.
const connectionLifecycleErrorCode = "connection_lifecycle"

// FailureKind is the normalized category used for retry and credential-state
// decisions. The HTTP status alone is not sufficient because the same status
// can represent an edge failure, an account failure, or a request failure.
type FailureKind string

const (
	FailureKindUnknownTransient FailureKind = "unknown_transient"
	FailureKindRequestFault     FailureKind = "request_fault"
	FailureKindAuth             FailureKind = "auth"
	FailureKindQuota            FailureKind = "quota"
	FailureKindEdgeGateway      FailureKind = "edge_gateway"
	FailureKindCapacity         FailureKind = "capacity"
	FailureKindTransport        FailureKind = "transport"
	FailureKindStreamQuality    FailureKind = "stream_quality"
)

const maxFailureEvidenceBodyBytes = 8 * 1024

// FailureEvidence contains bounded runtime-only evidence used to classify an
// upstream failure. It is excluded from persisted JSON because it can contain
// response headers and provider error text.
type FailureEvidence struct {
	Headers       http.Header `json:"-"`
	Body          []byte      `json:"-"`
	OutputStarted bool        `json:"-"`
}

type errorRuntimeState struct {
	mu          sync.RWMutex
	failureKind FailureKind
	evidence    *FailureEvidence
}

type errorHandlingStartKey struct{}

func withErrorHandlingStart(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, errorHandlingStartKey{}, time.Now())
}

func errorHandlingStart(ctx context.Context) time.Time {
	if ctx == nil {
		return time.Time{}
	}
	started, _ := ctx.Value(errorHandlingStartKey{}).(time.Time)
	return started
}

// ErrorHandlingMetricsSnapshot is a point-in-time view of request error
// handling telemetry. Counters are intentionally process-local and are not
// persisted with credentials.
type ErrorHandlingMetricsSnapshot struct {
	TemporaryFailures            uint64
	EdgeGatewayFailures          uint64
	CapacityFailures             uint64
	TransportFailures            uint64
	StreamFailures               uint64
	UnknownTransientFailures     uint64
	RetrySwitches                uint64
	RetryWaitMilliseconds        uint64
	RetriesBlockedAfterOutput    uint64
	FirstOutputCount             uint64
	FirstOutputDelayMilliseconds uint64
}

type errorHandlingMetrics struct {
	temporaryFailures            atomic.Uint64
	edgeGatewayFailures          atomic.Uint64
	capacityFailures             atomic.Uint64
	transportFailures            atomic.Uint64
	streamFailures               atomic.Uint64
	unknownTransientFailures     atomic.Uint64
	retrySwitches                atomic.Uint64
	retryWaitMilliseconds        atomic.Uint64
	retriesBlockedAfterOutput    atomic.Uint64
	firstOutputCount             atomic.Uint64
	firstOutputDelayMilliseconds atomic.Uint64
}

func (metrics *errorHandlingMetrics) recordTemporaryFailure(kind FailureKind) {
	if metrics == nil {
		return
	}
	metrics.temporaryFailures.Add(1)
	switch kind {
	case FailureKindEdgeGateway:
		metrics.edgeGatewayFailures.Add(1)
	case FailureKindCapacity:
		metrics.capacityFailures.Add(1)
	case FailureKindTransport:
		metrics.transportFailures.Add(1)
	case FailureKindStreamQuality:
		metrics.streamFailures.Add(1)
	case FailureKindUnknownTransient:
		metrics.unknownTransientFailures.Add(1)
	}
}

func (metrics *errorHandlingMetrics) snapshot() ErrorHandlingMetricsSnapshot {
	if metrics == nil {
		return ErrorHandlingMetricsSnapshot{}
	}
	return ErrorHandlingMetricsSnapshot{
		TemporaryFailures:            metrics.temporaryFailures.Load(),
		EdgeGatewayFailures:          metrics.edgeGatewayFailures.Load(),
		CapacityFailures:             metrics.capacityFailures.Load(),
		TransportFailures:            metrics.transportFailures.Load(),
		StreamFailures:               metrics.streamFailures.Load(),
		UnknownTransientFailures:     metrics.unknownTransientFailures.Load(),
		RetrySwitches:                metrics.retrySwitches.Load(),
		RetryWaitMilliseconds:        metrics.retryWaitMilliseconds.Load(),
		RetriesBlockedAfterOutput:    metrics.retriesBlockedAfterOutput.Load(),
		FirstOutputCount:             metrics.firstOutputCount.Load(),
		FirstOutputDelayMilliseconds: metrics.firstOutputDelayMilliseconds.Load(),
	}
}

const maxRuntimeErrorStates = 4096

var errorRuntimeStates = struct {
	sync.Mutex
	entries map[*Error]*errorRuntimeState
}{entries: make(map[*Error]*errorRuntimeState)}

func runtimeStateForError(err *Error, create bool) *errorRuntimeState {
	if err == nil {
		return nil
	}
	errorRuntimeStates.Lock()
	defer errorRuntimeStates.Unlock()
	if state := errorRuntimeStates.entries[err]; state != nil {
		return state
	}
	if !create {
		return nil
	}
	if len(errorRuntimeStates.entries) >= maxRuntimeErrorStates {
		// Classification evidence is request-local. Evict an arbitrary old
		// entry once the bounded cap is reached so failures cannot grow memory
		// without limit on a long-running gateway.
		for key := range errorRuntimeStates.entries {
			delete(errorRuntimeStates.entries, key)
			break
		}
	}
	state := &errorRuntimeState{}
	errorRuntimeStates.entries[err] = state
	return state
}

func errorFailureKind(err *Error) FailureKind {
	if state := runtimeStateForError(err, false); state != nil {
		state.mu.RLock()
		defer state.mu.RUnlock()
		return state.failureKind
	}
	return ""
}

func setErrorFailureKind(err *Error, kind FailureKind) {
	if state := runtimeStateForError(err, true); state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.failureKind = kind
	}
}

func errorEvidence(err *Error) *FailureEvidence {
	if state := runtimeStateForError(err, false); state != nil {
		state.mu.RLock()
		defer state.mu.RUnlock()
		return state.evidence
	}
	return nil
}

func setErrorEvidence(err *Error, evidence *FailureEvidence) {
	if state := runtimeStateForError(err, true); state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.evidence = evidence
	}
}

func (e *FailureEvidence) clone() *FailureEvidence {
	if e == nil {
		return nil
	}
	out := &FailureEvidence{OutputStarted: e.OutputStarted}
	if e.Headers != nil {
		out.Headers = e.Headers.Clone()
	}
	if len(e.Body) > 0 {
		if len(e.Body) > maxFailureEvidenceBodyBytes {
			out.Body = append([]byte(nil), e.Body[:maxFailureEvidenceBodyBytes]...)
		} else {
			out.Body = append([]byte(nil), e.Body...)
		}
	}
	return out
}

// Error describes an authentication related failure in a provider agnostic format.
type Error struct {
	// Code is a short machine readable identifier.
	Code string `json:"code,omitempty"`
	// Message is a human readable description of the failure.
	Message string `json:"message"`
	// Retryable indicates whether a retry might fix the issue automatically.
	Retryable bool `json:"retryable"`
	// HTTPStatus optionally records an HTTP-like status code for the error.
	HTTPStatus int `json:"http_status,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// StatusCode implements optional status accessor for manager decision making.
func (e *Error) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}

// IsRequestScoped reports whether the failure is tied to the current request
// rather than the selected credential.
func (e *Error) IsRequestScoped() bool {
	return e != nil && e.Code == requestScopedErrorCode
}
