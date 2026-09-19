package middleware

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/video_preprocess"

	"github.com/gin-gonic/gin"
)

// VideoPreprocess is the middleware that integrates the video preprocessing
// pipeline into the POST /v1/chat/completions route. It sits after
// ModelRequestRateLimit and before Distribute.
//
// The middleware:
//   - Calls video_preprocess.Processor.Process() with the gin context
//   - Maps all error codes to OpenAI-compatible HTTP responses
//   - Publishes context metadata for the AI Router and billing
//   - Distinguishes bypass (preprocessed=false) from successful processing
func VideoPreprocess() gin.HandlerFunc {
	return func(c *gin.Context) {
		processor := video_preprocess.GetProcessor()
		if processor == nil {
			abortWithOpenAiMessage(c, http.StatusInternalServerError,
				"video preprocessing unavailable",
				types.ErrorCodeInvalidApiType)
			return
		}

		model := c.GetString(string(constant.ContextKeyOriginalModel))
		userID := c.GetInt(string(constant.ContextKeyUserId))

		result, err := processor.Process(c.Request.Context(), video_preprocess.ProcessInput{
			UserID: userID,
			Model:  model,
			Ctx:    c,
		})

		if err != nil {
			handleVideoPreprocessError(c, err)
			return
		}

		publishVideoMetadata(c, result)
	}
}

// handleVideoPreprocessError maps video preprocessing errors to OpenAI-compatible
// HTTP responses. Admission errors carry their own HTTP status and Retry-After
// hint; all others are classified by their stable error code.
func handleVideoPreprocessError(c *gin.Context, err error) {
	code := video_preprocess.CodeOf(err)
	message := video_preprocess.MessageOf(err)
	if message == "" {
		message = fmt.Sprintf("video preprocessing failed: %s", code)
	}

	// Admission errors carry their own HTTP status and Retry-After.
	httpStatus := video_preprocess.HTTPStatusForError(err)
	if retryAfter, ok := video_preprocess.RetryAfterForError(err); ok {
		c.Header("Retry-After", fmt.Sprintf("%d", retryAfter))
	}

	// Map the stable error code to an OpenAI error code.
	var openAICode types.ErrorCode
	switch code {
	case video_preprocess.CodeInvalidRequest,
		video_preprocess.CodeInvalidDataURL,
		video_preprocess.CodeInvalidBase64,
		video_preprocess.CodeUnsupportedFormat,
		video_preprocess.CodeInvalidConfig:
		openAICode = types.ErrorCodeInvalidRequest
		httpStatus = http.StatusBadRequest

	case video_preprocess.CodeTooLarge,
		video_preprocess.CodeTooLong,
		video_preprocess.CodeResolutionExceeded,
		video_preprocess.CodeOutputTooLarge:
		openAICode = types.ErrorCodeBadRequestBody
		httpStatus = http.StatusRequestEntityTooLarge

	case video_preprocess.CodeQueueFull,
		video_preprocess.CodeQueueBytes,
		video_preprocess.CodeUserLimit,
		video_preprocess.CodeEstimatedWait:
		openAICode = types.ErrorCodeBadRequestBody
		httpStatus = http.StatusTooManyRequests

	case video_preprocess.CodeStorageExhausted,
		video_preprocess.CodeDiskStorageExhausted:
		openAICode = types.ErrorCodeBadRequestBody
		httpStatus = http.StatusServiceUnavailable

	case video_preprocess.CodeProcessingTimeout,
		video_preprocess.CodeQueueTimeout:
		openAICode = types.ErrorCodeBadRequestBody
		httpStatus = http.StatusRequestTimeout

	case video_preprocess.CodeProcessingCanceled:
		openAICode = types.ErrorCodeBadRequestBody
		httpStatus = 499

	default:
		openAICode = types.ErrorCodeBadRequestBody
		httpStatus = http.StatusInternalServerError
	}

	abortWithOpenAiMessage(c, httpStatus, message, openAICode)
}

// publishVideoMetadata writes the ProcessResult into the gin context so the
// AI Router, the Relay layer and billing can read what happened.
//
// The contract by outcome:
//   - Bypass (Bypassed=true): original_modality is set, preprocessed=false.
//   - Success (Bypassed=false): all metadata is present, preprocessed=true.
func publishVideoMetadata(c *gin.Context, result *video_preprocess.ProcessResult) {
	if result.Bypassed {
		// Bypass: publish only what was observed, never assume a rewrite.
		common.SetContextKey(c, constant.ContextKeyVideoPreprocessed, false)
		if result.OriginalModality != "" {
			common.SetContextKey(c, constant.ContextKeyVideoOriginalModality, result.OriginalModality)
			common.SetContextKey(c, constant.ContextKeyVideoEffectiveModality, result.EffectiveModality)
		}
		return
	}

	// Success: publish all metadata.
	common.SetContextKey(c, constant.ContextKeyVideoPreprocessed, true)
	common.SetContextKey(c, constant.ContextKeyVideoOriginalModality, result.OriginalModality)
	common.SetContextKey(c, constant.ContextKeyVideoEffectiveModality, result.EffectiveModality)
	common.SetContextKey(c, constant.ContextKeyVideoCount, result.VideoCount)
	common.SetContextKey(c, constant.ContextKeyVideoExistingImages, result.ExistingImages)
	common.SetContextKey(c, constant.ContextKeyVideoExtractedFrames, result.ExtractedFrames)
	common.SetContextKey(c, constant.ContextKeyVideoDecodeMS, result.DecodeMS)
	common.SetContextKey(c, constant.ContextKeyVideoProbeMS, result.ProbeMS)
	common.SetContextKey(c, constant.ContextKeyVideoExportMS, result.ExportMS)
	common.SetContextKey(c, constant.ContextKeyVideoRewriteMS, result.RewriteMS)
	common.SetContextKey(c, constant.ContextKeyVideoTotalMS, result.TotalMS)
	common.SetContextKey(c, constant.ContextKeyVideoSamplerVersion, result.SamplerVersion)
	common.SetContextKey(c, constant.ContextKeyVideoOriginalBodyBytes, result.OriginalBodyBytes)
	common.SetContextKey(c, constant.ContextKeyVideoRewrittenBodyBytes, result.RewrittenBodyBytes)
}
