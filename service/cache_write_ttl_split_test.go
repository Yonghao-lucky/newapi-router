package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cacheWriteTTLHeader(fiveMinute, oneHour string) http.Header {
	header := http.Header{}
	if fiveMinute != "" {
		header.Set(MoonshotCacheWrite5mHeader, fiveMinute)
	}
	if oneHour != "" {
		header.Set(MoonshotCacheWrite1hHeader, oneHour)
	}
	return header
}

func TestApplyCacheWriteTTLSplitFromHeader(t *testing.T) {
	chatUsage := dto.Usage{PromptTokensDetails: dto.InputTokenDetails{CacheWriteTokens: 2816}}
	tests := []struct {
		name      string
		header5m  string
		header1h  string
		usage     dto.Usage
		want5m    int
		want1h    int
		wantSplit bool
	}{
		{name: "1h write as reported by the upstream", header5m: "0", header1h: "2816", usage: chatUsage, want1h: 2816, wantSplit: true},
		{name: "5m write as reported by the upstream", header5m: "2816", header1h: "0", usage: chatUsage, want5m: 2816, wantSplit: true},
		{name: "both buckets are taken from their own headers", header5m: "1816", header1h: "1000", usage: chatUsage, want5m: 1816, want1h: 1000, wantSplit: true},
		{name: "headers exceeding the body total are bounded by it", header5m: "999999999", header1h: "999999999", usage: chatUsage, want1h: 2816, wantSplit: true},
		{name: "zero headers keep the total-only usage", header5m: "0", header1h: "0", usage: chatUsage},
		{name: "missing headers keep the total-only usage", usage: chatUsage},
		{name: "malformed headers are ignored", header5m: "abc", header1h: "-5", usage: chatUsage},
		{name: "no cache writes in the body", header5m: "0", header1h: "2816", usage: dto.Usage{}},
		{
			name:     "anthropic usage already carries its own split",
			header5m: "0",
			header1h: "2816",
			usage: dto.Usage{
				UsageSemantic:               dto.BillingUsageSemanticAnthropic,
				ClaudeCacheCreation5mTokens: 2816,
				PromptTokensDetails:         dto.InputTokenDetails{CachedCreationTokens: 2816},
			},
			want5m: 2816,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := tt.usage

			ApplyCacheWriteTTLSplitFromHeader(cacheWriteTTLHeader(tt.header5m, tt.header1h), &usage)

			assert.Equal(t, tt.want5m, usage.ClaudeCacheCreation5mTokens)
			assert.Equal(t, tt.want1h, usage.ClaudeCacheCreation1hTokens)
			if tt.wantSplit {
				assert.Equal(t, dto.BillingUsageSemanticOpenAI, usage.UsageSemantic)
			}
		})
	}
}

func TestApplyCacheWriteTTLSplitFromHeaderUpdatesOpenAIBillingUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokensDetails: dto.InputTokenDetails{CacheWriteTokens: 800},
		BillingUsage: &dto.BillingUsage{
			Source: dto.BillingUsageSourceOAIChat,
			OpenAIUsage: &dto.Usage{
				PromptTokensDetails: dto.InputTokenDetails{CacheWriteTokens: 800},
			},
		},
	}

	ApplyCacheWriteTTLSplitFromHeader(cacheWriteTTLHeader("300", "500"), usage)

	billingUsage := effectiveBillingUsage(usage)
	require.NotNil(t, billingUsage)
	assert.Equal(t, 300, billingUsage.ClaudeCacheCreation5mTokens)
	assert.Equal(t, 500, billingUsage.ClaudeCacheCreation1hTokens)
}

// Chat-completions cache writes must be priced by the upstream-reported TTL,
// matching what the same writes cost via /v1/messages.
func TestBuildTieredTokenParamsPricesOpenAICacheWriteTTLSplit(t *testing.T) {
	expr := `tier("base", p * 13 + c * 68 + cr * 1.3 + cc * 13 + cc1h * 26)`
	tests := []struct {
		name     string
		header5m string
		header1h string
		wantCost float64
	}{
		// p = 2818 - 2816 = 2: (2*13 + 16*68 + 2816*26) * 0.5
		{name: "1h write", header5m: "0", header1h: "2816", wantCost: 37165},
		// (2*13 + 16*68 + 2816*13) * 0.5
		{name: "5m write", header5m: "2816", header1h: "0", wantCost: 18861},
		// (2*13 + 16*68 + 1816*13 + 1000*26) * 0.5
		{name: "mixed 5m and 1h", header5m: "1816", header1h: "1000", wantCost: 25361},
		// No headers: unchanged total-only pricing at the 5m rate.
		{name: "no headers", wantCost: 18861},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := &dto.Usage{
				PromptTokens:        2818,
				CompletionTokens:    16,
				PromptTokensDetails: dto.InputTokenDetails{CacheWriteTokens: 2816},
			}
			ApplyCacheWriteTTLSplitFromHeader(cacheWriteTTLHeader(tt.header5m, tt.header1h), usage)

			assert.InDelta(t, tt.wantCost, tieredQuota(expr, usage, false, 1.0), 0.01)
		})
	}
}

func TestCalculateTextQuotaSummaryPricesOpenAICacheWriteTTLSplit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "kimi-k3.1-pro",
		PriceData: hosttypes.PriceData{
			ModelRatio:           1,
			CompletionRatio:      2,
			CacheRatio:           0.1,
			CacheCreationRatio:   1.25,
			CacheCreation5mRatio: 1.25,
			CacheCreation1hRatio: 2,
			GroupRatioInfo:       hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}
	usage := &dto.Usage{
		PromptTokens:        1473,
		CompletionTokens:    19,
		PromptTokensDetails: dto.InputTokenDetails{CacheWriteTokens: 1470},
	}
	ApplyCacheWriteTTLSplitFromHeader(cacheWriteTTLHeader("1000", "470"), usage)

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.Equal(t, 1470, summary.CacheCreationTokens)
	assert.Equal(t, 1000, summary.CacheCreationTokens5m)
	assert.Equal(t, 470, summary.CacheCreationTokens1h)
	assert.Equal(t, 1470, cacheWriteTokensTotal(summary))
	// (1473-1470) + 1000*1.25 + 470*2 + 19*2 = 3 + 1250 + 940 + 38 = 2231
	assert.Equal(t, 2231, summary.Quota)
}
