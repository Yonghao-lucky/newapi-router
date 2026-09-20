package common

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouterResponseBudget(t *testing.T) {
	for _, tc := range []struct {
		name               string
		progress, complete bool
	}{
		{"silent", false, false}, {"productive stream", true, false}, {"complete result", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, b := NewRouterResponseBudget(context.Background(), 10*time.Second)
				defer b.Close()
				if tc.complete {
					require.NoError(t, b.Complete())
				}
				for range 3 {
					time.Sleep(6 * time.Second) // virtual clock, no real waiting
					if tc.progress {
						b.Progress()
					}
				}
				if tc.progress || tc.complete {
					assert.NoError(t, ctx.Err())
				} else {
					assert.ErrorIs(t, context.Cause(ctx), ErrRouterIdleTimeout)
					assert.Equal(t, 504, b.APIError().StatusCode)
				}
				if tc.progress {
					time.Sleep(11 * time.Second)
					synctest.Wait()
					assert.ErrorIs(t, context.Cause(ctx), ErrRouterIdleTimeout)
				}
			})
		})
	}
}

func TestRouterProgressRecognizesGenerationOnly(t *testing.T) {
	for _, tc := range []struct {
		data string
		want bool
	}{
		{`: PING`, false}, {`{"choices":[{"delta":{"role":"assistant","content":""}}]}`, false},
		{`{"usage":{"completion_tokens":10}}`, false}, {`{"error":{"message":"failure"}}`, false},
		{`{"choices":[{"delta":{"content":"hello"}}]}`, true},
		{`{"choices":[{"delta":{"reasoning_content":"think"}}]}`, true},
		{`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]}}]}`, true},
		{`{"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`, false},
	} {
		assert.Equal(t, tc.want, IsGenerationEvent([]byte(tc.data)), tc.data)
	}
}

func TestRouterCompletionAndTimeoutHaveOneWinner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, b := NewRouterResponseBudget(context.Background(), time.Second)
		defer b.Close()
		time.Sleep(time.Second)
		synctest.Wait()
		require.ErrorIs(t, b.Complete(), ErrRouterIdleTimeout)
		b.Progress()
		assert.ErrorIs(t, context.Cause(ctx), ErrRouterIdleTimeout)
	})
}
