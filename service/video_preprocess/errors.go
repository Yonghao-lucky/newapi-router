// Package video_preprocess implements the standalone media engine behind the
// video request preprocessing system: a Base64 video Data URL is decoded to a
// request-scoped temporary file, probed with ffprobe, sampled into ascending
// uniform timestamps and exported as JPEG frames by batch FFmpeg invocations.
//
// The package owns no global configuration, no queue, no request rewriting and
// no relay integration. Callers pass their limits in through Options and
// receive frames plus explicit ownership of the files on disk.
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
)

// Error is the typed failure returned by every entry point of this package.
type Error struct {
	Code string

	// Message is safe to log and to return to an operator. It never contains
	// Base64 data, a full Data URL, customer media or a live temporary path.
	Message string

	// cause is retained for errors.Is/errors.As and for local debugging only.
	// The Cleanup plan requires callers to classify on Code and Message, not
	// on Error(), and must never emit Error() back to a client.
	cause error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return e.Code + ": " + e.Message
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// CodeOf reports the stable video preprocessing code carried by err, or an
// empty string when err did not come from this package.
func CodeOf(err error) string {
	var vErr *Error
	if errors.As(err, &vErr) {
		return vErr.Code
	}
	return ""
}

// MessageOf reports the operator-safe message carried by err. Errors from
// outside this package are reported as an empty string so that no unrelated
// error text is accidentally surfaced.
func MessageOf(err error) string {
	var vErr *Error
	if errors.As(err, &vErr) {
		return vErr.Message
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

// invalidConfig reports a caller-supplied option that the engine cannot use.
func invalidConfig(format string, args ...any) error {
	return newError(CodeInvalidConfig, fmt.Sprintf(format, args...))
}
