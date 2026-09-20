package middleware

import (
	"io"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/ai_router"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"
	"github.com/gin-gonic/gin"
)

// AIRouterResponseBudget starts after upload but before media queues and billing.
func AIRouterResponseBudget() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !ai_router_setting.GetSettings().Enabled {
			return
		}
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return
		} // existing validators retain their error contract
		reader, err := storage.NewReader()
		if err != nil {
			return
		}
		inspection, err := ai_router.InspectChatRequest(reader)
		_ = reader.Close()
		c.Request.Body = io.NopCloser(storage)
		if err != nil || !ai_router.ShouldUseRouter(c, inspection.Demand.Model, c.Request.URL.Path) {
			return
		}
		ctx, budget := common.NewRouterResponseBudget(c.Request.Context(), time.Duration(ai_router_setting.GetSettings().IdleTimeoutSeconds)*time.Second)
		c.Request = c.Request.WithContext(ctx)
		defer budget.Close()
		c.Next()
	}
}
