package model

import (
	"fmt"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// The old policy schema intentionally lacks the new column: upgrading it must
// preserve candidate links and uniqueness, and leave existing policies off.
type legacyStrictPolicy struct {
	Id          int    `gorm:"primaryKey"`
	Model       string `gorm:"type:varchar(191);uniqueIndex:uk_router_target_model_group"`
	GroupId     string `gorm:"type:varchar(191);uniqueIndex:uk_router_target_model_group"`
	Enabled     bool
	MaxAttempts int
	CreatedAt   int64 `gorm:"bigint"`
	UpdatedAt   int64 `gorm:"bigint"`
}

func (legacyStrictPolicy) TableName() string { return "ai_router_target_policies" }

func TestStrictPolicyMigrationDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := DB
			if dialect != "sqlite" {
				dsn := os.Getenv("AI_ROUTER_TEST_MYSQL_DSN")
				if dialect == "postgres" {
					dsn = os.Getenv("AI_ROUTER_TEST_POSTGRES_DSN")
				}
				if dsn == "" {
					t.Skip("isolated database DSN not configured")
				}
				var dialector gorm.Dialector = mysql.Open(dsn)
				if dialect == "postgres" {
					dialector = postgres.Open(dsn)
				}
				var err error
				db, err = gorm.Open(dialector, &gorm.Config{})
				require.NoError(t, err)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			}
			for _, upgrade := range []bool{false, true} {
				t.Run(fmt.Sprintf("upgrade=%t", upgrade), func(t *testing.T) {
					caseDB := db
					if dialect != "sqlite" {
						var err error
						caseDB, err = gorm.Open(db.Dialector, &gorm.Config{})
						require.NoError(t, err)
						sqlDB, err := caseDB.DB()
						require.NoError(t, err)
						t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
					}
					db := caseDB
					require.NoError(t, db.Migrator().DropTable(&AIRouterTargetCandidate{}, &AIRouterTargetPolicy{}))
					if upgrade {
						require.NoError(t, db.AutoMigrate(&legacyStrictPolicy{}, &AIRouterTargetCandidate{}))
						require.NoError(t, db.Create(&legacyStrictPolicy{Id: 99, Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 3}).Error)
						require.NoError(t, db.Create(&AIRouterTargetCandidate{Id: 98, PolicyId: 99, ChannelId: 42, Enabled: true}).Error)
					}
					for range 2 {
						require.NoError(t, db.AutoMigrate(&AIRouterTargetPolicy{}, &AIRouterTargetCandidate{}))
					}
					if !upgrade {
						require.NoError(t, db.Create(&AIRouterTargetPolicy{Id: 99, Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 3}).Error)
					}
					var policy AIRouterTargetPolicy
					require.NoError(t, db.Preload("Candidates").First(&policy, 99).Error)
					assert.False(t, policy.K3StrictValidation)
					assert.Equal(t, 3, policy.MaxAttempts)
					if upgrade {
						require.Len(t, policy.Candidates, 1)
						assert.Equal(t, 42, policy.Candidates[0].ChannelId)
					}
					for _, enabled := range []bool{true, false} {
						require.NoError(t, db.Model(&policy).Update("k3_strict_validation", enabled).Error)
						require.NoError(t, db.First(&policy, 99).Error)
						assert.Equal(t, enabled, policy.K3StrictValidation)
					}
					require.Error(t, db.Create(&AIRouterTargetPolicy{Model: "kimi-k3", GroupId: "default"}).Error)
					if upgrade {
						require.Error(t, db.Create(&AIRouterTargetCandidate{PolicyId: 99, ChannelId: 42}).Error)
					}
				})
			}
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&AIRouterTargetCandidate{}, &AIRouterTargetPolicy{}))
				require.NoError(t, db.AutoMigrate(&AIRouterTargetPolicy{}, &AIRouterTargetCandidate{}))
			})
		})
	}
}

// The target policy tables back the V1.5 Router scheduling contract:
// (model, group) is unique, and (policy, channel) is unique per candidate.

func TestTargetPolicyUniqueModelGroup(t *testing.T) {
	t.Cleanup(func() {
		DB.Exec("DELETE FROM ai_router_target_policies")
		DB.Exec("DELETE FROM ai_router_target_candidates")
	})
	DB.Exec("DELETE FROM ai_router_target_policies")
	DB.Exec("DELETE FROM ai_router_target_candidates")

	first := &AIRouterTargetPolicy{Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 3}
	require.NoError(t, DB.Create(first).Error)

	duplicate := &AIRouterTargetPolicy{Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 3}
	require.Error(t, DB.Create(duplicate).Error)

	otherGroup := &AIRouterTargetPolicy{Model: "kimi-k3", GroupId: "vip", Enabled: true, MaxAttempts: 3}
	require.NoError(t, DB.Create(otherGroup).Error)
}

func TestTargetCandidateUniquePolicyChannel(t *testing.T) {
	t.Cleanup(func() {
		DB.Exec("DELETE FROM ai_router_target_policies")
		DB.Exec("DELETE FROM ai_router_target_candidates")
	})
	DB.Exec("DELETE FROM ai_router_target_policies")
	DB.Exec("DELETE FROM ai_router_target_candidates")

	policy := &AIRouterTargetPolicy{Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 3}
	require.NoError(t, DB.Create(policy).Error)

	first := &AIRouterTargetCandidate{PolicyId: policy.Id, ChannelId: 42, Enabled: true}
	require.NoError(t, DB.Create(first).Error)

	duplicate := &AIRouterTargetCandidate{PolicyId: policy.Id, ChannelId: 42, Enabled: true}
	require.Error(t, DB.Create(duplicate).Error)

	// The same channel may appear under a different policy.
	other := &AIRouterTargetCandidate{PolicyId: policy.Id + 1, ChannelId: 42, Enabled: true}
	require.NoError(t, DB.Create(other).Error)
}

func TestChannelRoutingProfileMaxRetryDefaultsAndBounds(t *testing.T) {
	// Absent max_retry defaults to zero: one call per channel.
	var absent dto.ChannelOtherSettings
	require.NoError(t, common.UnmarshalJsonStr(`{"router":{"enabled":true}}`, &absent))
	assert.Equal(t, 0, absent.Router.MaxRetry)

	inRange := dto.ChannelOtherSettings{}
	require.NoError(t, common.UnmarshalJsonStr(`{"router":{"enabled":true,"max_retry":3}}`, &inRange))
	assert.Equal(t, 3, inRange.Router.MaxRetry)
	require.NoError(t, inRange.ValidateRouter())

	overLimit := dto.ChannelOtherSettings{}
	require.NoError(t, common.UnmarshalJsonStr(`{"router":{"enabled":true,"max_retry":6}}`, &overLimit))
	require.Error(t, overLimit.ValidateRouter())

	negative := dto.ChannelOtherSettings{}
	require.NoError(t, common.UnmarshalJsonStr(`{"router":{"enabled":true,"max_retry":-1}}`, &negative))
	require.Error(t, negative.ValidateRouter())
}

func TestVideoPrioritySettingsDatabaseMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, dsn string
		dialector gorm.Dialector
	}{
		{name: "sqlite"},
		{name: "mysql", dsn: os.Getenv("AI_ROUTER_TEST_MYSQL_DSN"), dialector: mysql.Open(os.Getenv("AI_ROUTER_TEST_MYSQL_DSN"))},
		{name: "postgres", dsn: os.Getenv("AI_ROUTER_TEST_POSTGRES_DSN"), dialector: postgres.Open(os.Getenv("AI_ROUTER_TEST_POSTGRES_DSN"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := DB
			if tc.name != "sqlite" {
				if tc.dsn == "" {
					t.Skip("isolated database DSN not configured")
				}
				var err error
				db, err = gorm.Open(tc.dialector, &gorm.Config{})
				require.NoError(t, err)
				sqlDB, err := db.DB()
				require.NoError(t, err)
				defer sqlDB.Close()
			}
			previous := DB
			DB = db
			defer func() { DB = previous }()
			require.NoError(t, db.AutoMigrate(&Channel{}))
			ch := Channel{Name: "video-priority-fixture", Key: "mock-only", Group: "default", Models: "kimi-k3", OtherSettings: `{"router":{"enabled":true,"max_retry":2,"retry_status_codes":[],"switch_status_codes":[502]}}`}
			require.NoError(t, db.Create(&ch).Error)
			defer db.Delete(&ch)
			var before Channel
			require.NoError(t, db.First(&before, ch.Id).Error)
			legacySettings := before.GetOtherSettings()
			assert.Nil(t, legacySettings.RoutingProfile().VideoPriority)
			zero, high := int64(0), int64(100)
			for _, priority := range []*int64{&high, &zero, nil} {
				settings := before.GetOtherSettings()
				settings.Router.VideoPriority = priority
				before.SetOtherSettings(settings)
				require.NoError(t, db.Model(&Channel{}).Where("id = ?", ch.Id).Update("settings", before.OtherSettings).Error)
				infos, err := BuildTargetPolicyChannelInfos([]int{ch.Id})
				require.NoError(t, err)
				require.Contains(t, infos, ch.Id)
				assert.Equal(t, priority, infos[ch.Id].VideoPriority)
				assert.Equal(t, 2, infos[ch.Id].MaxRetry)
				assert.Equal(t, []int{}, infos[ch.Id].RetryStatusCodes)
				assert.Equal(t, []int{502}, infos[ch.Id].SwitchStatusCodes)
			}
		})
	}
}
