package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const routerIngressGroupKey = "ai_router_ingress_group"

// ResolveAIRouterGroup shares the existing auto-group selection with ingress.
// A strict request pins its resolved group so later cache changes cannot select
// a different validation policy after admission.
func ResolveAIRouterGroup(c *gin.Context, modelName, group string, filters []dto.ChannelFilter) string {
	if pinned := c.GetString(routerIngressGroupKey); pinned != "" {
		return pinned
	}
	if group == "" {
		return "default"
	}
	if group != "auto" {
		return group
	}
	userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	for _, autoGroup := range GetRequestAutoGroups(c, userGroup) {
		if len(model.GetEnabledChannelsForGroupModel(autoGroup, modelName, filters)) > 0 {
			common.SetContextKey(c, constant.ContextKeyAutoGroup, autoGroup)
			return autoGroup
		}
	}
	return group
}

func PinAIRouterIngressGroup(c *gin.Context, group string) { c.Set(routerIngressGroupKey, group) }
