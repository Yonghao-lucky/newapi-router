package constant

type ContextKey string

const (
	ContextKeyTokenCountMeta  ContextKey = "token_count_meta"
	ContextKeyPromptTokens    ContextKey = "prompt_tokens"
	ContextKeyEstimatedTokens ContextKey = "estimated_tokens"

	ContextKeyOriginalModel    ContextKey = "original_model"
	ContextKeyRequestStartTime ContextKey = "request_start_time"

	/* token related keys */
	ContextKeyTokenUnlimited         ContextKey = "token_unlimited_quota"
	ContextKeyTokenKey               ContextKey = "token_key"
	ContextKeyTokenId                ContextKey = "token_id"
	ContextKeyTokenGroup             ContextKey = "token_group"
	ContextKeyOriginTasks            ContextKey = "origin_tasks"
	ContextKeyChannelConstraints     ContextKey = "channel_constraints"
	ContextKeyTokenModelLimitEnabled ContextKey = "token_model_limit_enabled"
	ContextKeyTokenModelLimit        ContextKey = "token_model_limit"
	ContextKeyTokenCrossGroupRetry   ContextKey = "token_cross_group_retry"
	ContextKeyTokenAutoGroups        ContextKey = "token_auto_groups"

	/* channel related keys */
	ContextKeyChannelId                ContextKey = "channel_id"
	ContextKeyChannelName              ContextKey = "channel_name"
	ContextKeyChannelCreateTime        ContextKey = "channel_create_time"
	ContextKeyChannelBaseUrl           ContextKey = "base_url"
	ContextKeyChannelType              ContextKey = "channel_type"
	ContextKeyChannelSetting           ContextKey = "channel_setting"
	ContextKeyChannelOtherSetting      ContextKey = "channel_other_setting"
	ContextKeyChannelParamOverride     ContextKey = "param_override"
	ContextKeyChannelHeaderOverride    ContextKey = "header_override"
	ContextKeyChannelOrganization      ContextKey = "channel_organization"
	ContextKeyChannelAutoBan           ContextKey = "auto_ban"
	ContextKeyChannelModelMapping      ContextKey = "model_mapping"
	ContextKeyChannelStatusCodeMapping ContextKey = "status_code_mapping"
	ContextKeyChannelIsMultiKey        ContextKey = "channel_is_multi_key"
	ContextKeyChannelMultiKeyIndex     ContextKey = "channel_multi_key_index"
	ContextKeyChannelKey               ContextKey = "channel_key"

	ContextKeyAutoGroup           ContextKey = "auto_group"
	ContextKeyAutoGroupIndex      ContextKey = "auto_group_index"
	ContextKeyAutoGroupRetryIndex ContextKey = "auto_group_retry_index"

	/* user related keys */
	ContextKeyUserId      ContextKey = "id"
	ContextKeyUserSetting ContextKey = "user_setting"
	ContextKeyUserQuota   ContextKey = "user_quota"
	ContextKeyUserStatus  ContextKey = "user_status"
	ContextKeyUserEmail   ContextKey = "user_email"
	ContextKeyUserGroup   ContextKey = "user_group"
	ContextKeyUsingGroup  ContextKey = "group"
	ContextKeyUserName    ContextKey = "username"

	ContextKeyLocalCountTokens ContextKey = "local_count_tokens"

	ContextKeySystemPromptOverride ContextKey = "system_prompt_override"

	// ContextKeyFileSourcesToCleanup stores file sources that need cleanup when request ends
	ContextKeyFileSourcesToCleanup ContextKey = "file_sources_to_cleanup"

	// ContextKeyAdminRejectReason stores an admin-only reject/block reason extracted from upstream responses.
	// It is not returned to end users, but can be persisted into consume/error logs for debugging.
	ContextKeyAdminRejectReason ContextKey = "admin_reject_reason"

	// ContextKeyLanguage stores the user's language preference for i18n
	ContextKeyLanguage ContextKey = "language"
	ContextKeyIsStream ContextKey = "is_stream"

	// ContextKeyAuditLogged marks that the current request has already recorded
	// a manage/operation audit log inside the handler. When set, the admin-audit
	// fallback in authHelper (finishAdminAudit) skips its record to avoid
	// duplicate entries.
	ContextKeyAuditLogged ContextKey = "audit_logged"

	// ContextKeyTokenAuditParams contains only the API token operation's safe metadata.
	ContextKeyTokenAuditParams ContextKey = "token_audit_params"
	// ContextKeyTokenAuditSucceeded disambiguates token responses that exceed the audit buffer.
	ContextKeyTokenAuditSucceeded ContextKey = "token_audit_succeeded"
)

// AI Router request-scoped context keys.
const (
	ContextKeyAIRouterActive  ContextKey = "ai_router_active"
	ContextKeyAIRouterContext ContextKey = "ai_router_context"
)

// Video preprocessing request-scoped context keys. These report what the
// preprocessing subsystem observed and produced — never client media, Base64,
// prompt text or temporary paths — so later stages (AI Router, billing, logs)
// can act on the rewrite while retaining the original modality for metrics.
const (
	// ContextKeyVideoPreprocessed is true when video preprocessing ran and
	// rewrote the request. It is false on bypass paths (disabled switch,
	// non-target model, no video content) and absent when the feature was
	// never invoked. A bypass must not set this to true.
	ContextKeyVideoPreprocessed ContextKey = "video_preprocessed"

	// ContextKeyVideoOriginalModality reports what the request carried before
	// preprocessing: "video" when videos were found, "image" for image-only
	// requests, "text" otherwise. Empty on bypass-before-scan paths (disabled
	// switch, non-target model).
	ContextKeyVideoOriginalModality ContextKey = "video_original_modality"

	// ContextKeyVideoEffectiveModality reports what the AI Router and channel
	// selection must route on: "image" after a successful rewrite, otherwise
	// the original modality.
	ContextKeyVideoEffectiveModality ContextKey = "video_effective_modality"

	// ContextKeyVideoCount is the number of video_url parts the request carried.
	ContextKeyVideoCount ContextKey = "video_count"

	// ContextKeyVideoExistingImages is the number of image_url parts that were
	// already in the request before preprocessing.
	ContextKeyVideoExistingImages ContextKey = "video_existing_images"

	// ContextKeyVideoExtractedFrames is the total number of JPEG frames exported
	// from all videos.
	ContextKeyVideoExtractedFrames ContextKey = "video_extracted_frames"

	// ContextKeyVideoDecodeMS is the total decode time across all videos.
	ContextKeyVideoDecodeMS ContextKey = "video_decode_ms"

	// ContextKeyVideoProbeMS is the total probe time across all videos.
	ContextKeyVideoProbeMS ContextKey = "video_probe_ms"

	// ContextKeyVideoExportMS is the total export time across all videos.
	ContextKeyVideoExportMS ContextKey = "video_export_ms"

	// ContextKeyVideoRewriteMS is the request rewrite time.
	ContextKeyVideoRewriteMS ContextKey = "video_rewrite_ms"

	// ContextKeyVideoTotalMS is the end-to-end preprocessing time.
	ContextKeyVideoTotalMS ContextKey = "video_total_ms"

	// ContextKeyVideoSamplerVersion identifies the sampling integration that
	// produced the rewrite.
	ContextKeyVideoSamplerVersion ContextKey = "video_sampler_version"

	// ContextKeyVideoOriginalBodyBytes is the size of the request before
	// preprocessing.
	ContextKeyVideoOriginalBodyBytes ContextKey = "video_original_body_bytes"

	// ContextKeyVideoRewrittenBodyBytes is the size of the request after
	// preprocessing.
	ContextKeyVideoRewrittenBodyBytes ContextKey = "video_rewritten_body_bytes"
)
