package ai_router

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// ClassifyAttemptError is the single place that maps a relay error onto an
// ai_router_attempt_logs error category. Rules, in order:
//
//  1. no error            -> "success"
//  2. response had already started streaming and then failed
//     -> "stream_interrupted" (断流), whatever the cause
//  3. wrapped transport error is a timeout -> "timeout"
//  4. wrapped transport error is a connection failure -> "connection"
//  5. no upstream response at all (synthetic status) -> "connection"
//  6. real HTTP status: 429 -> "429", 5xx -> "5xx", anything else -> "other"
//
// It also returns the status code worth persisting: calls that never got a
// response (do_request_failed / get_channel_failed carry a synthetic 500)
// keep a NULL http_status_code instead of a misleading 500.
func ClassifyAttemptError(err *types.NewAPIError, responseStarted bool) (category string, statusCode *int) {
	if err == nil {
		return model.AttemptErrorCategorySuccess, nil
	}
	status := normalizedAttemptStatus(err)

	if responseStarted {
		return model.AttemptErrorCategoryStreamInterrupted, status
	}
	if isTimeoutError(err.Err) {
		return model.AttemptErrorCategoryTimeout, status
	}
	if isConnectionError(err.Err) {
		return model.AttemptErrorCategoryConnection, status
	}
	if status == nil {
		// The call failed before the upstream answered (dial refused, TLS
		// handshake, malformed URL, ...): there was no HTTP status.
		return model.AttemptErrorCategoryConnection, nil
	}
	switch {
	case *status == 429:
		return model.AttemptErrorCategory429, status
	case *status >= 500 && *status <= 599:
		return model.AttemptErrorCategory5xx, status
	default:
		return model.AttemptErrorCategoryOther, status
	}
}

// normalizedAttemptStatus returns the upstream HTTP status for an error, or
// nil when the status is synthetic (the call never got a response) or out of
// the HTTP range.
func normalizedAttemptStatus(err *types.NewAPIError) *int {
	if err == nil {
		return nil
	}
	switch err.GetErrorCode() {
	case types.ErrorCodeDoRequestFailed, types.ErrorCodeGetChannelFailed:
		return nil
	}
	if err.StatusCode >= 100 && err.StatusCode <= 599 {
		status := err.StatusCode
		return &status
	}
	return nil
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if isTimeoutError(err) {
		return false
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.ECONNABORTED),
		errors.Is(err, syscall.EPIPE),
		errors.Is(err, syscall.EHOSTUNREACH),
		errors.Is(err, syscall.ENETUNREACH),
		errors.Is(err, syscall.ENETDOWN),
		errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF):
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}
