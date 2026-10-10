package middleware

import (
	"errors"
	"io"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/ai_router"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"
	"github.com/gin-gonic/gin"
)

// AIRouterResponseBudget starts after upload but before media queues and billing.
func AIRouterResponseBudget() gin.HandlerFunc {
	return func(c *gin.Context) {
		settings := ai_router_setting.GetSettings()
		if !settings.Enabled {
			return
		}
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return
		} // existing validators retain their error contract
		snapshot := ai_router.GetTargetPolicySnapshot()
		inspection, err := ai_router.InspectStoredChatRequest(c, storage, snapshot.HasStrictValidation())
		c.Request.Body = io.NopCloser(storage)
		if c.Request.URL.Path != "/v1/chat/completions" {
			return
		}
		if !snapshot.HasStrictValidation() && (err != nil || !ai_router.ShouldUseRouter(c, inspection.Demand.Model, c.Request.URL.Path)) {
			return
		}
		if snapshot.HasStrictValidation() {
			ai_router.PinRequestPolicySnapshot(c, snapshot)
			filters := append([]dto.ChannelFilter(nil), service.GetChannelConstraints(c).Filters...)
			filters = append(filters, dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: c.Request.URL.Path})
			tokenGroup := common.GetContextKeyString(c, constant.ContextKeyTokenGroup)
			if tokenGroup == "" {
				tokenGroup = common.GetContextKeyString(c, constant.ContextKeyUserGroup)
			}
			modelNames := inspection.ModelNames
			if len(modelNames) == 0 {
				modelNames = []string{inspection.Demand.Model}
			}
			for _, modelName := range modelNames {
				if !ai_router.ShouldUseRouter(c, modelName, c.Request.URL.Path) {
					continue
				}
				if common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled) {
					limit, _ := common.GetContextKeyType[map[string]bool](c, constant.ContextKeyTokenModelLimit)
					if !tokenModelLimitAllows(limit, modelName) {
						continue
					}
				}
				group := service.ResolveAIRouterGroup(c, modelName, tokenGroup, filters)
				policy := snapshot.GetPolicy(modelName, group)
				if policy != nil && policy.Enabled && policy.K3StrictValidation {
					service.PinAIRouterIngressGroup(c, group)
					if err != nil {
						inspection.ValidationError = &ai_router.ChatValidationError{Message: "request must contain one valid JSON object"}
						var fieldErr *ai_router.ChatValidationError
						if errors.As(err, &fieldErr) {
							inspection.ValidationError = fieldErr
						}
					}
					if inspection.ValidationError != nil && inspection.ValidationError.Code != "unsupported_thinking_mode" {
						apiErr := inspection.ValidationError.APIError()
						c.AbortWithStatusJSON(apiErr.StatusCode, gin.H{"error": apiErr.ToOpenAIError()})
						return
					}
				}
			}
		}
		// Preserve the existing distributor's model-authorization response.
		// Content diagnostics must not reveal capabilities for a forbidden model.
		if common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled) {
			limit, _ := common.GetContextKeyType[map[string]bool](c, constant.ContextKeyTokenModelLimit)
			if !tokenModelLimitAllows(limit, inspection.Demand.Model) {
				return
			}
		}
		if ai_router.ShouldUseRouter(c, inspection.Demand.Model, c.Request.URL.Path) {
			var fieldErr *ai_router.ChatValidationError
			if errors.As(err, &fieldErr) {
				apiErr := fieldErr.APIError()
				c.AbortWithStatusJSON(apiErr.StatusCode, gin.H{"error": apiErr.ToOpenAIError()})
				return
			}
		}
		if err != nil || !ai_router.ShouldUseRouter(c, inspection.Demand.Model, c.Request.URL.Path) {
			return
		}
		// Content and capability failures precede media admission and billing.
		filters := append([]dto.ChannelFilter(nil), service.GetChannelConstraints(c).Filters...)
		filters = append(filters, dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: c.Request.URL.Path})
		tokenGroup := common.GetContextKeyString(c, constant.ContextKeyTokenGroup)
		if tokenGroup == "" {
			tokenGroup = common.GetContextKeyString(c, constant.ContextKeyUserGroup)
		}
		group := service.ResolveAIRouterGroup(c, inspection.Demand.Model, tokenGroup, filters)
		if apiErr := ai_router.AdmitRouteInput(&inspection.Demand, group, snapshot.GetPolicy(inspection.Demand.Model, group)); apiErr != nil {
			c.AbortWithStatusJSON(apiErr.StatusCode, gin.H{"error": apiErr.ToOpenAIError()})
			return
		}
		c.Set("ai_router_require_thinking_disabled", inspection.Demand.RequireThinkingDisabled)
		c.Set("ai_router_has_unknown_input", len(inspection.Demand.UnknownContentTypes) > 0)
		ctx, budget := common.NewRouterResponseBudget(c.Request.Context(), time.Duration(settings.IdleTimeoutSeconds)*time.Second)
		c.Request = c.Request.WithContext(ctx)
		defer budget.Close()
		c.Next()
	}
}
