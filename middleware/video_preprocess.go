package middleware

import (
	"errors"
	"io"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/ai_router"
	"github.com/QuantumNous/new-api/service/video_preprocess"

	"github.com/gin-gonic/gin"
)

// VideoPreprocess is the middleware that integrates the video preprocessing
// pipeline into the POST /v1/chat/completions route. It sits after
// ModelRequestRateLimit and before Distribute.
//
// The middleware:
//   - Extracts the model from the request body
//   - Calls video_preprocess.Processor.Process() with the gin context
//   - Maps all error codes to OpenAI-compatible HTTP responses
//   - Publishes context metadata for the AI Router and billing
//   - Distinguishes bypass (preprocessed=false) from successful processing
func VideoPreprocess() gin.HandlerFunc {
	return func(c *gin.Context) {
		processor := video_preprocess.GetProcessor()
		if processor == nil {
			handleVideoPreprocessError(c, &video_preprocess.Error{Code: video_preprocess.CodeInvalidConfig})
			return
		}
		if !processor.Enabled() {
			return
		}

		// Extract model from request body. This must happen before calling
		// Processor, because no middleware before VideoPreprocess sets
		// ContextKeyOriginalModel.
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			code := video_preprocess.CodeInvalidRequest
			if errors.Is(err, common.ErrRequestBodyTooLarge) || common.IsRequestBodyTooLargeError(err) {
				code = video_preprocess.CodeTooLarge
			}
			handleVideoPreprocessError(c, &video_preprocess.Error{Code: code})
			return
		}
		reader, err := storage.NewReader()
		if err != nil {
			handleVideoPreprocessError(c, &video_preprocess.Error{Code: video_preprocess.CodeInvalidRequest})
			return
		}
		inspection, err := ai_router.InspectChatRequest(reader)
		_ = reader.Close()
		if err != nil || inspection.Demand.Model == "" {
			handleVideoPreprocessError(c, &video_preprocess.Error{Code: video_preprocess.CodeInvalidRequest})
			return
		}
		c.Request.Body = io.NopCloser(storage)

		userID := c.GetInt(string(constant.ContextKeyUserId))

		result, err := processor.Process(c.Request.Context(), video_preprocess.ProcessInput{
			UserID:     userID,
			Model:      inspection.Demand.Model,
			Ctx:        c,
			Inspection: &inspection,
		})

		if err != nil {
			handleVideoPreprocessError(c, err)
			return
		}

		publishVideoMetadata(c, result)
	}
}

// handleVideoPreprocessError maps video preprocessing errors to OpenAI-compatible
// HTTP responses. The service owns classification and safe client messages.
func handleVideoPreprocessError(c *gin.Context, err error) {
	if budget := common.RouterBudget(c.Request.Context()); budget != nil {
		if apiErr := budget.APIError(); apiErr != nil {
			c.AbortWithStatusJSON(apiErr.StatusCode, gin.H{"error": apiErr.ToOpenAIError()})
			return
		}
	}
	code := video_preprocess.CodeOf(err)
	if code == "" {
		code = video_preprocess.CodeInternal
	}
	if retryAfter, ok := video_preprocess.RetryAfterForError(err); ok {
		c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
	}
	abortWithOpenAiMessage(c, video_preprocess.HTTPStatusForError(err),
		video_preprocess.PublicMessageForError(err), types.ErrorCode(code))
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
	common.SetContextKey(c, constant.ContextKeyVideoAnalyzeMS, result.AnalyzeMS)
	common.SetContextKey(c, constant.ContextKeyVideoSelectMS, result.SelectMS)
	common.SetContextKey(c, constant.ContextKeyVideoExportMS, result.ExportMS)
	common.SetContextKey(c, constant.ContextKeyVideoRewriteMS, result.RewriteMS)
	common.SetContextKey(c, constant.ContextKeyVideoTotalMS, result.TotalMS)
	common.SetContextKey(c, constant.ContextKeyVideoSamplerVersion, result.SamplerVersion)
	common.SetContextKey(c, constant.ContextKeyVideoOriginalBodyBytes, result.OriginalBodyBytes)
	common.SetContextKey(c, constant.ContextKeyVideoRewrittenBodyBytes, result.RewrittenBodyBytes)
}
