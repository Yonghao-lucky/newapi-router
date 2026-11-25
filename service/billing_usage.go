package service

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"net/http"
	"strconv"
	"strings"
)

const (
	usageBillingPathLocal              = "local"
	usageBillingPathUpstream           = "upstream"
	usageBillingPathOpenAI             = "billing-usage-openai"
	usageBillingPathOpenAIEstimated    = "billing-usage-openai-estimated"
	usageBillingPathAnthropic          = "billing-usage-anthropic"
	usageBillingPathAnthropicEstimated = "billing-usage-anthropic-estimated"
	usageBillingPathGemini             = "billing-usage-gemini"
	usageBillingPathGeminiEstimated    = "billing-usage-gemini-estimated"
)

// Moonshot-compatible upstreams report the per-TTL cache-write counts only in
// these response headers; OpenAI-format bodies carry just the total
// (prompt_tokens_details.cache_write_tokens).
const (
	MoonshotCacheWrite5mHeader = "Msh-Usage-Cache-Write-Tokens-5m"
	MoonshotCacheWrite1hHeader = "Msh-Usage-Cache-Write-Tokens-1h"
)

// ApplyCacheWriteTTLSplitFromHeader records the upstream's 5m/1h cache-write
// counts from the Msh-Usage-Cache-Write-Tokens-5m/1h response headers so each
// bucket is billed at its own cache-creation price, exactly as the upstream
// reports it. The headers are upstream-controlled, so their sum is bounded by
// the body's cache-write total. Usage that already carries a TTL split or a
// non-OpenAI semantic is left untouched.
func ApplyCacheWriteTTLSplitFromHeader(header http.Header, usage *dto.Usage) {
	if usage == nil || header == nil {
		return
	}
	fiveMinuteTokens, err5m := strconv.Atoi(strings.TrimSpace(header.Get(MoonshotCacheWrite5mHeader)))
	oneHourTokens, err1h := strconv.Atoi(strings.TrimSpace(header.Get(MoonshotCacheWrite1hHeader)))
	if err5m != nil || fiveMinuteTokens < 0 {
		fiveMinuteTokens = 0
	}
	if err1h != nil || oneHourTokens < 0 {
		oneHourTokens = 0
	}
	if fiveMinuteTokens == 0 && oneHourTokens == 0 {
		return
	}
	targets := []*dto.Usage{usage}
	if usage.BillingUsage != nil && usage.BillingUsage.OpenAIUsage != nil {
		targets = append(targets, usage.BillingUsage.OpenAIUsage)
	}
	for _, target := range targets {
		if target.UsageSemantic != "" && target.UsageSemantic != dto.BillingUsageSemanticOpenAI {
			continue
		}
		if target.ClaudeCacheCreation5mTokens > 0 || target.ClaudeCacheCreation1hTokens > 0 {
			continue
		}
		total := target.PromptTokensDetails.CacheCreationTokensTotal()
		if total <= 0 {
			continue
		}
		target.ClaudeCacheCreation1hTokens = min(oneHourTokens, total)
		target.ClaudeCacheCreation5mTokens = min(fiveMinuteTokens, total-target.ClaudeCacheCreation1hTokens)
		target.UsageSemantic = dto.BillingUsageSemanticOpenAI
	}
}

func effectiveBillingUsage(usage *dto.Usage) *dto.Usage {
	if billingUsage, ok := usageFromBillingUsage(usage); ok {
		return billingUsage
	}
	return usage
}

func usageBillingPathForLog(isLocalCountTokens bool, usage *dto.Usage) string {
	effectiveUsage, ok := usageFromBillingUsage(usage)
	if !ok {
		if isLocalCountTokens {
			return usageBillingPathLocal
		}
		return usageBillingPathUpstream
	}

	switch effectiveUsage.UsageSemantic {
	case dto.BillingUsageSemanticOpenAI:
		if usage.BillingUsage.Estimated {
			return usageBillingPathOpenAIEstimated
		}
		return usageBillingPathOpenAI
	case dto.BillingUsageSemanticAnthropic:
		if usage.BillingUsage.Estimated {
			return usageBillingPathAnthropicEstimated
		}
		return usageBillingPathAnthropic
	case dto.BillingUsageSemanticGemini:
		if usage.BillingUsage.Estimated {
			return usageBillingPathGeminiEstimated
		}
		return usageBillingPathGemini
	}

	return usageBillingPathUpstream
}

func appendUsageBillingPathForLog(other *model.LogOther, isLocalCountTokens bool, usage *dto.Usage) {
	if other == nil {
		return
	}
	other.SetAdmin("usage_billing_path", usageBillingPathForLog(isLocalCountTokens, usage))
}

func usageFromBillingUsage(usage *dto.Usage) (*dto.Usage, bool) {
	if usage == nil || usage.BillingUsage == nil {
		return nil, false
	}
	return usage.BillingUsage.CanonicalUsage()
}
