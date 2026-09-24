// Package video_preprocess turns a Chat Completions request that carries
// Base64 video Data URLs into an equivalent request whose videos have been
// replaced by text markers and JPEG `image_url` parts, ready for the ordinary
// image routing of the AI Router.
//
// The package is organized in three layers:
//
//   - the media engine (data_url, probe, exporter, timestamps): decodes a
//     Data URL to a request-scoped temporary file, probes it with ffprobe and
//     exports JPEG frames with batch FFmpeg invocations;
//   - the request rewriter (scan, rewrite, storage): locates video content,
//     splices frames back in place, enforces the image and body budgets and
//     installs the result as a replayable BodyStorage;
//   - the admission queue (admission, job): a bounded, per-user-fair queue
//     that meters the heavy pipeline.
//
// The request-level Processor composes the three layers. None of the layers
// reads global configuration; callers map their settings onto the limit types
// and pass them in.
package video_preprocess

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Stable error codes. They are part of the module contract: the middleware,
// the admin API and the task log all classify failures by these strings, so
// existing values must not be renamed. Upstream fallback must not retry any of
// them, because switching channels cannot repair a local video failure.
const (
	// Rewriter and media engine codes.
	CodeInvalidRequest       = "video_invalid_request"
	CodeInvalidDataURL       = "video_invalid_data_url"
	CodeInvalidBase64        = "video_invalid_base64"
	CodeUnsupportedFormat    = "video_unsupported_format"
	CodeProbeFailed          = "video_probe_failed"
	CodeTooLarge             = "video_too_large"
	CodeTooLong              = "video_too_long"
	CodeResolutionExceeded   = "video_resolution_exceeded"
	CodeDiskStorageExhausted = "video_disk_storage_exhausted"
	CodeDecodeFailed         = "video_decode_failed"
	CodeExportFailed         = "video_export_failed"
	CodeImageLimitExceeded   = "video_image_limit_exceeded"
	CodeOutputTooLarge       = "video_output_too_large"
	CodeStorageFailed        = "video_storage_failed"
	CodeRewriteFailed        = "video_rewrite_failed"
	CodeInvalidConfig        = "video_invalid_config"

	// Admission and lifecycle codes.
	CodeQueueFull        = "video_queue_full"
	CodeQueueBytes       = "video_queue_bytes_exceeded"
	CodeUserLimit        = "video_user_queue_limit"
	CodeEstimatedWait    = "video_estimated_wait_exceeded"
	CodeStorageExhausted = "video_storage_exhausted"
	CodeQueueTimeout     = "video_queue_timeout"

	// Shared lifecycle codes.
	CodeProcessingTimeout  = "video_processing_timeout"
	CodeProcessingCanceled = "video_processing_canceled"
	CodeProcessingFailed   = "video_processing_failed"
	CodeInternal           = "video_processing_internal"
)

// Error is the typed failure returned by every media engine and rewriting
// entry point of this package.
type Error struct {
	Code string

	// Message is safe to log and to return to an operator. It never contains
	// Base64 data, a full Data URL, customer media or a live temporary path.
	Message string

	// cause is retained for errors.Is/errors.As and for local debugging only.
	// Callers classify on Code and Message, and must never emit Error() back to
	// a client.
	cause error
	// Only constructors with fixed templates may set this client-facing detail.
	publicMessage string
}

func (e *Error) Error() string {
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// VideoPreprocessCode lets the admission layer's StatusForError and the HTTP
// mappings classify a media or rewrite failure through one interface, so the
// whole pipeline has a single classification entry point.
func (e *Error) VideoPreprocessCode() string { return e.Code }

// VideoPreprocessMessage is the operator-safe counterpart of
// VideoPreprocessCode.
func (e *Error) VideoPreprocessMessage() string { return e.Message }

// CodeOf reports the stable video preprocessing code carried by err, or an
// empty string when err did not come from this package. It recognizes every
// error type of the package — media, rewriting, admission and stage errors —
// so a single switch in the middleware can classify the whole pipeline.
func CodeOf(err error) string {
	var pErr *Error
	if errors.As(err, &pErr) {
		return pErr.Code
	}
	var admission *AdmissionError
	if errors.As(err, &admission) {
		return admission.Code
	}
	var stage *StageError
	if errors.As(err, &stage) {
		if stage.Code != "" {
			return stage.Code
		}
		if stage.Status == JobStatusCanceled {
			return CodeProcessingCanceled
		}
		if stage.Status == JobStatusTimedOut {
			return CodeProcessingTimeout
		}
		return CodeProcessingFailed
	}
	var coder interface{ VideoPreprocessCode() string }
	if errors.As(err, &coder) {
		return coder.VideoPreprocessCode()
	}
	return ""
}

// MessageOf reports the operator-safe message carried by err. Errors from
// outside this package are reported as an empty string so that no unrelated
// error text is accidentally surfaced.
func MessageOf(err error) string {
	var pErr *Error
	if errors.As(err, &pErr) {
		return pErr.Message
	}
	var admission *AdmissionError
	if errors.As(err, &admission) {
		return admission.Message
	}
	var stage *StageError
	if errors.As(err, &stage) {
		return stage.Message
	}
	var messager interface{ VideoPreprocessMessage() string }
	if errors.As(err, &messager) {
		return messager.VideoPreprocessMessage()
	}
	return ""
}

// newError builds a coded error without a wrapped cause.
func newError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// wrapError builds a coded error that keeps cause available to local
// errors.Is/errors.As checks. cause is never rendered by Error().
func wrapError(code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

// invalidConfig reports a caller-supplied option that the engine, the
// rewriter or the queue cannot use.
func invalidConfig(format string, args ...any) error {
	return newError(CodeInvalidConfig, fmt.Sprintf(format, args...))
}

// timeoutOrCancel maps a request context failure onto the module error it
// should be reported as. It returns nil while the context is still healthy.
func timeoutOrCancel(ctx context.Context) error {
	switch {
	case ctx == nil:
		return nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return newError(CodeProcessingTimeout, "video preprocessing exceeded its time budget")
	case errors.Is(ctx.Err(), context.Canceled):
		return newError(CodeProcessingCanceled, "video preprocessing was canceled")
	default:
		return nil
	}
}

// AdmissionError is an admission-time rejection. It is a terminal task outcome
// and carries what the middleware needs to answer the client: a stable code, an
// HTTP status and a Retry-After hint.
type AdmissionError struct {
	Code           string
	Message        string
	HTTPStatus     int
	RetryAfterSecs int64
}

func (e *AdmissionError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func newAdmissionError(code, message string, status int, retryAfterSecs int64) *AdmissionError {
	return &AdmissionError{
		Code:           code,
		Message:        message,
		HTTPStatus:     status,
		RetryAfterSecs: max(retryAfterSecs, 0),
	}
}

// StageError is a pipeline failure. It wraps whatever caused it — a context
// cancellation, a panic recovered at the stage boundary, or an error returned
// by the stage handler — and keeps the media error code stable for the
// integration layer.
//
// Status is the terminal job status the producer asserts. It is set explicitly
// by the producer that knows the cause (the queue sets canceled or timed out;
// the media layer leaves it empty for an ordinary failure) instead of being
// inferred from the wrapped error, because a stage may report a sub-operation
// deadline for a job that is otherwise healthy.
type StageError struct {
	Code    string
	Stage   JobStage
	Status  JobStatus
	Err     error
	Message string
}

func (e *StageError) Error() string {
	if e == nil {
		return ""
	}
	stage := string(e.Stage)
	if stage == "" {
		stage = "pipeline"
	}
	if e.Err == nil {
		return fmt.Sprintf("video_preprocess %s: %s", stage, e.Message)
	}
	return fmt.Sprintf("video_preprocess %s: %s: %v", stage, e.Message, e.Err)
}

func (e *StageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StatusForError maps an error to a terminal status and stable code. Media and
// rewriting errors are recognized through their VideoPreprocessCode interface;
// errors raised outside this package are classified as processing failures so
// the caller still gets an auditable code.
func StatusForError(err error) (JobStatus, string) {
	if err == nil {
		return JobStatusSucceeded, ""
	}

	var admission *AdmissionError
	if errors.As(err, &admission) {
		return JobStatusRejected, admission.Code
	}

	var stage *StageError
	if errors.As(err, &stage) {
		status := stage.Status
		if status == "" {
			status = JobStatusFailed
		}
		if stage.Code != "" {
			return status, stage.Code
		}
		if status == JobStatusCanceled {
			return status, CodeProcessingCanceled
		}
		if status == JobStatusTimedOut {
			return status, CodeProcessingTimeout
		}
		return status, CodeProcessingFailed
	}

	var coder interface{ VideoPreprocessCode() string }
	if errors.As(err, &coder) {
		return JobStatusFailed, coder.VideoPreprocessCode()
	}

	if errors.Is(err, context.Canceled) {
		return JobStatusCanceled, CodeProcessingCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return JobStatusTimedOut, CodeProcessingTimeout
	}
	if strings.TrimSpace(err.Error()) == "" {
		return JobStatusFailed, CodeInternal
	}
	return JobStatusFailed, CodeProcessingFailed
}

// HTTPStatusForError maps an error to the status the client should receive.
// Errors that reach the upstream relay are never retried on another channel.
func HTTPStatusForError(err error) int {
	switch CodeOf(err) {
	case CodeInvalidRequest, CodeInvalidDataURL, CodeInvalidBase64,
		CodeUnsupportedFormat, CodeTooLong, CodeResolutionExceeded, CodeImageLimitExceeded:
		return http.StatusBadRequest
	case CodeTooLarge, CodeOutputTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeQueueFull, CodeQueueBytes, CodeUserLimit, CodeEstimatedWait:
		return http.StatusTooManyRequests
	case CodeStorageExhausted, CodeDiskStorageExhausted, CodeStorageFailed,
		CodeProbeFailed, CodeDecodeFailed, CodeExportFailed, CodeInvalidConfig:
		return http.StatusServiceUnavailable
	case CodeProcessingTimeout, CodeQueueTimeout:
		return http.StatusRequestTimeout
	case CodeProcessingCanceled:
		return 499
	default:
		status, _ := StatusForError(err)
		if status == JobStatusCanceled {
			return 499
		}
		if status == JobStatusTimedOut {
			return http.StatusRequestTimeout
		}
		return http.StatusInternalServerError
	}
}

// PublicMessageForError only exposes fixed allowlisted messages or details
// assembled by safe constructors. Arbitrary diagnostics are never reflected.
func PublicMessageForError(err error) string {
	var detail *Error
	if errors.As(err, &detail) && detail.publicMessage != "" {
		return detail.publicMessage
	}
	switch message := MessageOf(err); message {
	case "video MIME type is not enabled", "video codec is not enabled",
		"video media is invalid or damaged",
		"video container format is not enabled", "downloaded video container is not enabled",
		"video MIME type contradicts its container", "decoded video has no recognizable container magic",
		"media has no video stream", "only one video and at most one audio stream are supported",
		"video duration could not be determined", "video dimensions could not be determined",
		"video frame rate could not be determined safely", "video rotation must be a right angle",
		"remote video URLs are disabled", "remote video size is unknown and unknown sizes are disabled",
		"remote video URLs must use port 443", "remote video hostname is not in the allowed list",
		"remote video hostname resolves to a non-public address", "remote video URL hostname is not allowed",
		"remote video requires an HTTPS URL without credentials or a fragment",
		"remote video exceeded the redirect limit", "remote video redirect contains a fragment",
		"remote video hostname could not be resolved", "remote video server could not be reached",
		"remote video HTTP request failed", "remote video content encoding is not supported",
		"remote video range response is invalid", "remote video range size is invalid",
		"remote video range response has an invalid length", "remote video exceeds the configured byte limit",
		"remote video size changed between probe and download", "remote video download ended before completion",
		"remote video download length does not match the expected size":
		return message
	}
	switch CodeOf(err) {
	case CodeInvalidBase64:
		return "video Base64 payload is invalid"
	case CodeTooLong:
		return "video duration exceeds the configured limit"
	case CodeResolutionExceeded:
		return "video dimensions or pixel count exceed the configured limit"
	case CodeImageLimitExceeded:
		return "video frames and existing images exceed the configured image limit"
	case CodeOutputTooLarge:
		return "video frames or rewritten request exceed the configured output byte limit"
	case CodeTooLarge:
		return "video input size or analyzed frame count exceeds the configured limit"
	}
	switch HTTPStatusForError(err) {
	case http.StatusBadRequest:
		return "video input is invalid or exceeds the configured media constraints"
	case http.StatusRequestEntityTooLarge:
		return "video request or output exceeds the configured byte limit"
	case http.StatusTooManyRequests:
		return "video preprocessing capacity is currently exhausted"
	case http.StatusServiceUnavailable:
		return "video preprocessing is temporarily unavailable"
	case http.StatusRequestTimeout:
		return "video preprocessing exceeded its time budget"
	case 499:
		return "video preprocessing was canceled"
	default:
		return "video preprocessing failed"
	}
}

// RetryAfterForError returns a positive hint for every capacity rejection.
func RetryAfterForError(err error) (int64, bool) {
	var admission *AdmissionError
	if errors.As(err, &admission) && admission.RetryAfterSecs > 0 {
		return admission.RetryAfterSecs, true
	}
	if HTTPStatusForError(err) == http.StatusTooManyRequests {
		return 1, true
	}
	return 0, false
}

// contextFailure classifies a context error. The job timeout is derived from the
// caller's context, so a deadline here is always the job timeout, and any other
// context error is the caller going away.
func contextFailure(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &StageError{
			Code:    CodeProcessingTimeout,
			Status:  JobStatusTimedOut,
			Err:     err,
			Message: "processing deadline exceeded",
		}
	}
	return &StageError{
		Code:    CodeProcessingCanceled,
		Status:  JobStatusCanceled,
		Err:     err,
		Message: "request context canceled",
	}
}

// mediaCommandError distinguishes recognizable damaged input from a local tool
// failure without publishing or retaining subprocess diagnostics.
func mediaCommandError(code string, cause error, diagnostic string) error {
	for _, marker := range []string{"Invalid data found when processing input", "moov atom not found", "Invalid NAL unit", "Error while decoding stream", "corrupt decoded frame", "partial file"} {
		if strings.Contains(diagnostic, marker) {
			return wrapError(CodeUnsupportedFormat, "video media is invalid or damaged", cause)
		}
	}
	return wrapError(code, "video media tool failed", cause)
}
