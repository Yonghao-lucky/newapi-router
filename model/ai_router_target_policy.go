package model

// AIRouterTargetPolicy answers "which channels may serve this (model, group)".
// It is the V1.5 replacement of the V1 global target_models list: policies are
// per model+group and carry their own candidate set. priority/weight/group are
// NOT copied here — they keep using the Channel's own fields as the single
// source of truth.
type AIRouterTargetPolicy struct {
	Id                   int                        `json:"id" gorm:"primaryKey"`
	Model                string                     `json:"model" gorm:"type:varchar(191);uniqueIndex:uk_router_target_model_group"`
	GroupId              string                     `json:"group_id" gorm:"type:varchar(191);uniqueIndex:uk_router_target_model_group"`
	Enabled              bool                       `json:"enabled"`
	MaxAttempts          int                        `json:"max_attempts"`
	K3StrictValidation   bool                       `json:"k3_strict_validation"`
	RejectEnableThinking bool                       `json:"reject_enable_thinking"`
	CreatedAt            int64                      `json:"created_at" gorm:"bigint"`
	UpdatedAt            int64                      `json:"updated_at" gorm:"bigint"`
	Candidates           []*AIRouterTargetCandidate `json:"candidates,omitempty" gorm:"foreignKey:PolicyId"`
}

func (AIRouterTargetPolicy) TableName() string {
	return "ai_router_target_policies"
}

// AIRouterTargetCandidate links a policy to a channel that may serve it.
// It deliberately stores only the link and its enable flag; scheduling and
// capability data stay on the Channel itself.
type AIRouterTargetCandidate struct {
	Id        int   `json:"id" gorm:"primaryKey"`
	PolicyId  int   `json:"policy_id" gorm:"uniqueIndex:uk_router_policy_channel"`
	ChannelId int   `json:"channel_id" gorm:"uniqueIndex:uk_router_policy_channel;index:idx_router_candidate_channel"`
	Enabled   bool  `json:"enabled"`
	CreatedAt int64 `json:"created_at" gorm:"bigint"`
	UpdatedAt int64 `json:"updated_at" gorm:"bigint"`
}

func (AIRouterTargetCandidate) TableName() string {
	return "ai_router_target_candidates"
}
