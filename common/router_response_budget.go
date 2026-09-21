package common

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

var ErrRouterIdleTimeout = errors.New("ai router: no effective response within the configured idle timeout")

type routerBudgetKey struct{}

// RouterResponseBudget spans preprocessing and every upstream attempt. Only
// actual generation progress resets it; transport activity never does.
type RouterResponseBudget struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelCauseFunc
	timer     *time.Timer
	interval  time.Duration
	deadline  time.Time
	completed bool
	delivered bool
	writeErr  error
}

func NewRouterResponseBudget(parent context.Context, interval time.Duration) (context.Context, *RouterResponseBudget) {
	ctx, cancel := context.WithCancelCause(parent)
	b := &RouterResponseBudget{ctx: ctx, cancel: cancel, interval: interval, deadline: time.Now().Add(interval)}
	b.timer = time.AfterFunc(interval, b.expire)
	return context.WithValue(ctx, routerBudgetKey{}, b), b
}

func RouterBudget(ctx context.Context) *RouterResponseBudget {
	if ctx == nil {
		return nil
	}
	b, _ := ctx.Value(routerBudgetKey{}).(*RouterResponseBudget)
	return b
}

func RouterBudgetFor(c *gin.Context) *RouterResponseBudget {
	if c == nil || c.Request == nil {
		return nil
	}
	return RouterBudget(c.Request.Context())
}

// FlushRouterResponse avoids Gin's void Flush swallowing an underlying
// FlushError. A successful buffered Write alone is not proof of delivery.
func FlushRouterResponse(c *gin.Context) error {
	w := http.ResponseWriter(c.Writer)
	if unwrapped, ok := w.(interface{ Unwrap() http.ResponseWriter }); ok {
		w = unwrapped.Unwrap()
	}
	return http.NewResponseController(w).Flush()
}

func (b *RouterResponseBudget) expire() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.completed || b.ctx.Err() != nil {
		return
	}
	if remaining := time.Until(b.deadline); remaining > 0 {
		b.timer.Reset(remaining)
		return
	}
	b.cancel(ErrRouterIdleTimeout)
}

func (b *RouterResponseBudget) Progress() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.completed || b.ctx.Err() != nil {
		return
	}
	if !time.Now().Before(b.deadline) {
		b.cancel(ErrRouterIdleTimeout)
		return
	}
	b.deadline = time.Now().Add(b.interval)
	b.timer.Reset(b.interval)
}

// Complete arbitrates a full valid result against timeout, before writing it.
func (b *RouterResponseBudget) Complete() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.completed && !time.Now().Before(b.deadline) {
		b.cancel(ErrRouterIdleTimeout)
	}
	if err := context.Cause(b.ctx); err != nil {
		return err
	}
	b.completed = true
	b.timer.Stop()
	return nil
}

// CompleteStream marks a stream that reached its provider terminal event as
// complete. The client connection may be closed immediately after the final
// event, which cancels the request context while the relay is still recording
// its terminal state. That cancellation must not turn a delivered stream into
// a synthetic 499; timeout and write failures still win.
func (b *RouterResponseBudget) CompleteStream() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.completed {
		return nil
	}
	if !time.Now().Before(b.deadline) {
		b.cancel(ErrRouterIdleTimeout)
	}
	if err := context.Cause(b.ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	b.completed = true
	b.timer.Stop()
	return nil
}

func (b *RouterResponseBudget) Close() {
	b.mu.Lock()
	b.completed = true
	b.timer.Stop()
	b.cancel(context.Canceled)
	b.mu.Unlock()
}
func (b *RouterResponseBudget) Delivered() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.delivered }
func (b *RouterResponseBudget) MarkDelivered()  { b.mu.Lock(); b.delivered = true; b.mu.Unlock() }
func (b *RouterResponseBudget) WriteFailed(err error) {
	b.mu.Lock()
	b.writeErr = err
	b.cancel(err)
	b.mu.Unlock()
}

func (b *RouterResponseBudget) APIError() *types.NewAPIError {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.writeErr != nil {
		return types.WithOpenAIError(types.OpenAIError{Type: "server_error", Code: "ai_router_response_write_failed", Message: b.writeErr.Error()}, 499, types.ErrOptionWithSkipRetry())
	}
	if b.completed {
		return nil
	}
	err := context.Cause(b.ctx)
	if err == nil {
		return nil
	}
	code, status := "ai_router_request_canceled", 499
	if errors.Is(err, ErrRouterIdleTimeout) {
		code, status = "ai_router_idle_timeout", 504
	}
	return types.WithOpenAIError(types.OpenAIError{Type: "server_error", Code: code, Message: err.Error()}, status, types.ErrOptionWithSkipRetry())
}

// IsGenerationEvent recognizes semantic SSE payloads, never comments, role-only
// frames, usage frames or errors. Used for progress and successful delivery.
func IsGenerationEvent(data []byte) bool {
	var event map[string]any
	if Unmarshal(data, &event) != nil || event["error"] != nil {
		return false
	}
	if choices, ok := event["choices"].([]any); ok {
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			if generationDelta(delta) {
				return true
			}
		}
	}
	if delta, ok := event["delta"].(map[string]any); ok {
		return generationDelta(delta)
	}
	if eventType, _ := event["type"].(string); eventType == "response.output_text.delta" || eventType == "response.reasoning_text.delta" || eventType == "response.reasoning_summary_text.delta" || eventType == "response.function_call_arguments.delta" {
		text, _ := event["delta"].(string)
		return text != ""
	}
	if message, ok := event["message"].(map[string]any); ok && generationDelta(message) {
		return true
	}
	if candidates, ok := event["candidates"].([]any); ok {
		for _, item := range candidates {
			candidate, _ := item.(map[string]any)
			content, _ := candidate["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, item := range parts {
				part, _ := item.(map[string]any)
				if text, _ := part["text"].(string); text != "" {
					return true
				}
				if call, _ := part["functionCall"].(map[string]any); call != nil {
					if name, _ := call["name"].(string); name != "" {
						return true
					}
				}
			}
		}
	}
	return false
}

func generationDelta(delta map[string]any) bool {
	for _, key := range []string{"content", "reasoning_content", "reasoning", "text", "thinking", "partial_json"} {
		if value, ok := delta[key].(string); ok && value != "" {
			return true
		}
	}
	if tools, ok := delta["tool_calls"].([]any); ok {
		for _, item := range tools {
			tool, _ := item.(map[string]any)
			function, _ := tool["function"].(map[string]any)
			for _, key := range []string{"name", "arguments"} {
				if text, ok := function[key].(string); ok && text != "" {
					return true
				}
			}
		}
	}
	if function, ok := delta["function_call"].(map[string]any); ok {
		for _, key := range []string{"name", "arguments"} {
			if text, ok := function[key].(string); ok && text != "" {
				return true
			}
		}
	}
	return false
}

const RouterUpstreamStatusKey = "ai_router_upstream_status"
const RouterRetryAfterKey = "ai_router_retry_after"
