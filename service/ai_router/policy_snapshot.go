package ai_router

import (
	"sync/atomic"
)

// TargetPolicyCandidate is the immutable per-channel view a snapshot holds.
type TargetPolicyCandidate struct {
	ChannelID int
	Enabled   bool
}

// TargetPolicy is the immutable view of one (model, group) policy.
type TargetPolicy struct {
	Id          int
	Model       string
	GroupId     string
	Enabled     bool
	MaxAttempts int
	Candidates  []TargetPolicyCandidate
}

// TargetPolicySnapshot is the published runtime view of all target policies.
// The request hot path reads it without touching the policy/candidate tables.
// Instances must be treated as immutable: callers never mutate a snapshot,
// and publishers always build a fresh one from committed data.
type TargetPolicySnapshot struct {
	// policies maps "model\x00group_id" to its policy. The NUL separator
	// cannot appear in either identifier, so no pair can alias another.
	policies map[string]*TargetPolicy
}

func targetPolicyKey(model, groupID string) string {
	return model + "\x00" + groupID
}

// GetPolicy returns the policy for a (model, group) pair, or nil.
func (s *TargetPolicySnapshot) GetPolicy(model, groupID string) *TargetPolicy {
	if s == nil {
		return nil
	}
	return s.policies[targetPolicyKey(model, groupID)]
}

// Policies returns every policy in the snapshot, ordered by insertion of the
// underlying map is not guaranteed — callers needing stable order must sort.
func (s *TargetPolicySnapshot) Policies() []*TargetPolicy {
	if s == nil {
		return nil
	}
	result := make([]*TargetPolicy, 0, len(s.policies))
	for _, p := range s.policies {
		result = append(result, p)
	}
	return result
}

func (s *TargetPolicySnapshot) Len() int {
	if s == nil {
		return 0
	}
	return len(s.policies)
}

var targetPolicySnapshot atomic.Pointer[TargetPolicySnapshot]

// GetTargetPolicySnapshot returns the currently published snapshot.
func GetTargetPolicySnapshot() *TargetPolicySnapshot {
	return targetPolicySnapshot.Load()
}

// PublishTargetPolicySnapshot atomically replaces the runtime snapshot.
func PublishTargetPolicySnapshot(snapshot *TargetPolicySnapshot) {
	targetPolicySnapshot.Store(snapshot)
}

// BuildTargetPolicySnapshot builds an immutable snapshot from stored policies,
// deep-copying candidates so nothing is shared with mutable objects.
func BuildTargetPolicySnapshot(policies []TargetPolicy) *TargetPolicySnapshot {
	snapshot := &TargetPolicySnapshot{policies: make(map[string]*TargetPolicy, len(policies))}
	for i := range policies {
		p := policies[i]
		candidates := make([]TargetPolicyCandidate, len(p.Candidates))
		copy(candidates, p.Candidates)
		stored := p
		stored.Candidates = candidates
		snapshot.policies[targetPolicyKey(stored.Model, stored.GroupId)] = &stored
	}
	return snapshot
}
