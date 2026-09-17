package ai_router

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
)

func TestClassifyAttemptErrorNilIsSuccess(t *testing.T) {
	category, status := ClassifyAttemptError(nil, false)
	if category != "success" {
		t.Errorf("category = %s, want success", category)
	}
	if status != nil {
		t.Errorf("status = %d, want nil", *status)
	}
}

func TestClassifyAttemptErrorStreamInterrupted(t *testing.T) {
	// Any failure after the response started streaming is 断流, whatever
	// the underlying cause (here: a 502 that arrived mid-stream).
	err := types.NewErrorWithStatusCode(errors.New("read: connection reset"), types.ErrorCodeBadResponseStatusCode, 502)
	category, status := ClassifyAttemptError(err, true)
	if category != "stream_interrupted" {
		t.Errorf("category = %s, want stream_interrupted", category)
	}
	if status == nil || *status != 502 {
		t.Errorf("status = %v, want 502", status)
	}
}

func TestClassifyAttemptErrorTimeout(t *testing.T) {
	cases := []error{
		context.DeadlineExceeded,
		fmt.Errorf("do request: %w", context.DeadlineExceeded),
		&net.DNSError{Err: "i/o timeout", IsTimeout: true},
		wrapOpError(syscall.ETIMEDOUT, true),
	}
	for i, cause := range cases {
		err := types.NewError(cause, types.ErrorCodeDoRequestFailed)
		category, status := ClassifyAttemptError(err, false)
		if category != "timeout" {
			t.Errorf("case %d: category = %s, want timeout", i, category)
		}
		if status != nil {
			t.Errorf("case %d: status = %d, want nil (no upstream response)", i, *status)
		}
	}
}

func TestClassifyAttemptErrorConnection(t *testing.T) {
	cases := []error{
		syscall.ECONNREFUSED,
		fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED),
		wrapOpError(syscall.ECONNRESET, false),
	}
	for i, cause := range cases {
		err := types.NewError(cause, types.ErrorCodeDoRequestFailed)
		category, status := ClassifyAttemptError(err, false)
		if category != "connection" {
			t.Errorf("case %d: category = %s, want connection", i, category)
		}
		if status != nil {
			t.Errorf("case %d: status = %d, want nil", i, *status)
		}
	}
}

func TestClassifyAttemptErrorHTTPStatuses(t *testing.T) {
	cases := []struct {
		status   int
		category string
	}{
		{429, "429"},
		{500, "5xx"},
		{503, "5xx"},
		{400, "other"},
		{401, "other"},
	}
	for _, tc := range cases {
		err := types.NewErrorWithStatusCode(errors.New("upstream error"), types.ErrorCodeBadResponseStatusCode, tc.status)
		category, status := ClassifyAttemptError(err, false)
		if category != tc.category {
			t.Errorf("status %d: category = %s, want %s", tc.status, category, tc.category)
		}
		if status == nil || *status != tc.status {
			t.Errorf("status %d: got %v, want %d", tc.status, status, tc.status)
		}
	}
}

func TestClassifyAttemptErrorSyntheticStatusStaysNull(t *testing.T) {
	// do_request_failed carries a synthetic 500 even for dial failures;
	// the attempt row must not record a misleading HTTP 500.
	err := types.NewError(errors.New("some transport trouble"), types.ErrorCodeDoRequestFailed)
	err.StatusCode = 500
	category, status := ClassifyAttemptError(err, false)
	if category != "connection" {
		t.Errorf("category = %s, want connection", category)
	}
	if status != nil {
		t.Errorf("status = %d, want nil", *status)
	}
}

func wrapOpError(errno syscall.Errno, timeout bool) error {
	op := &net.OpError{
		Op:  "dial",
		Err: osTimeoutWrap{errno, timeout},
	}
	return op
}

type osTimeoutWrap struct {
	err     error
	timeout bool
}

func (w osTimeoutWrap) Error() string   { return w.err.Error() }
func (w osTimeoutWrap) Unwrap() error   { return w.err }
func (w osTimeoutWrap) Timeout() bool   { return w.timeout }
func (w osTimeoutWrap) Temporary() bool { return false }
