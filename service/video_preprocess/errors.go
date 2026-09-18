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
)

// Stable error codes. They are part of the module contract: the middleware,
// the admin API and the task log all classify failures by these strings, so
// existing values must not be renamed.
const (
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
	CodeProcessingTimeout    = "video_processing_timeout"
	CodeProcessingCanceled   = "video_processing_canceled"
	CodeStorageFailed        = "video_storage_failed"
	CodeInvalidConfig        = "video_invalid_config"
	CodeRewriteFailed        = "video_rewrite_failed"
)

// Error is the typed failure returned by every media and rewriting entry point
// of this package.
type Error struct {
	Code string

	// Message is safe to log and to return to an operator. It never contains
	// Base64 data, a full Data URL, customer media or a live temporary path.
	Message string

	// cause is retained for errors.Is/errors.As and for local debugging only.
	// Callers classify on Code and Message, and must never emit Error() back to
	// a client.
	cause error
}

func (e *Error) Error() string {
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// VideoPreprocessCode lets the admission layer's StatusForError and the HTTP
// mappings classify a media or rewrite failure without importing this
// package's concrete type — one stable classification entry point for the
// whole pipeline.
func (e *Error) VideoPreprocessCode() string { return e.Code }

// VideoPreprocessMessage is the operator-safe counterpart of
// VideoPreprocessCode.
func (e *Error) VideoPreprocessMessage() string { return e.Message }

// CodeOf reports the stable video preprocessing code carried by err, or an
// empty string when err did not come from this package. It recognizes the
// package's own errors and, through the classification interfaces, any error
// another layer of this package produced, so a single switch in the middleware
// can classify the whole pipeline.
func CodeOf(err error) string {
	var pErr *Error
	if errors.As(err, &pErr) {
		return pErr.Code
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

// invalidConfig reports a caller-supplied option that the engine or the
// rewriter cannot use.
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
