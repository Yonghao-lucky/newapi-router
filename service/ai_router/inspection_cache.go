package ai_router

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

const inspectionCacheKey = "ai_router_chat_inspection"
const policySnapshotKey = "ai_router_ingress_policy_snapshot"

func PinRequestPolicySnapshot(c *gin.Context, snapshot *TargetPolicySnapshot) {
	c.Set(policySnapshotKey, snapshot)
}

func RequestPolicySnapshot(c *gin.Context) *TargetPolicySnapshot {
	if value, ok := c.Get(policySnapshotKey); ok {
		if snapshot, ok := value.(*TargetPolicySnapshot); ok {
			return snapshot
		}
	}
	return GetTargetPolicySnapshot()
}

type cachedChatInspection struct {
	body       common.BodyStorage
	inspection ChatInspection
	validated  bool
}

// CachedStoredChatDemand avoids rescanning an admitted unchanged body. When no
// clean inspection exists, callers keep the original detector's behavior.
func CachedStoredChatDemand(c *gin.Context, storage common.BodyStorage) (RouteDemand, bool) {
	value, ok := c.Get(inspectionCacheKey)
	if !ok {
		return RouteDemand{}, false
	}
	cached, ok := value.(cachedChatInspection)
	if !ok || cached.body != storage || cached.inspection.ValidationError != nil {
		return RouteDemand{}, false
	}
	return cached.inspection.Demand, true
}

// InspectStoredChatRequest reuses only an inspection of the identical body
// storage. Video rewriting replaces storage, so it cannot reuse stale metadata.
func InspectStoredChatRequest(c *gin.Context, storage common.BodyStorage, validate bool) (ChatInspection, error) {
	if value, ok := c.Get(inspectionCacheKey); ok {
		if cached, ok := value.(cachedChatInspection); ok && cached.body == storage && (!validate || cached.validated) {
			// A non-matching policy must retain the original inspector's
			// ambiguity errors (duplicate model/messages), not a deferred error.
			if validate || cached.inspection.ValidationError == nil {
				return cached.inspection, nil
			}
		}
	}
	reader, err := storage.NewReader()
	if err != nil {
		return ChatInspection{}, err
	}
	defer reader.Close()
	inspection, err := inspectChatRequest(reader, validate)
	if err == nil {
		c.Set(inspectionCacheKey, cachedChatInspection{storage, inspection, validate})
	}
	return inspection, err
}
