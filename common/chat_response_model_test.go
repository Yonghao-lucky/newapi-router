package common_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatResponseModelOnlyChangesTopLevelString(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"bytes", " { \"model\" : \"FW-Kimi-K3\", \"choices\": [], \"n\":1.00e+10, \"large\":9007199254740993 }\n", " { \"model\" : \"kimi-k3\", \"choices\": [], \"n\":1.00e+10, \"large\":9007199254740993 }\n"},
		{"nested", `{"choices":[{"delta":{"content":"FW-Kimi-K3","reasoning_content":"model","tool_calls":[{"function":{"arguments":"{\"model\":\"FW-Kimi-K3\"}"}}]}}],"extra":{"model":"FW-Kimi-K3"},"model":"FW-Kimi-K3"}`, `{"choices":[{"delta":{"content":"FW-Kimi-K3","reasoning_content":"model","tool_calls":[{"function":{"arguments":"{\"model\":\"FW-Kimi-K3\"}"}}]}}],"extra":{"model":"FW-Kimi-K3"},"model":"kimi-k3"}`},
		{"escaped key", `{"mo\u0064el":"FW-Kimi-K3","choices":[]}`, `{"mo\u0064el":"kimi-k3","choices":[]}`},
		{"same", `{"model":"kimi-k3","choices":[]}`, ""},
		{"same escaped", `{"model":"kimi\u002dk3","choices":[]}`, ""},
		{"missing", `{"choices":[],"usage":{"total_tokens":123}}`, ""},
		{"null", `{"model":null,"choices":[]}`, ""},
		{"number", `{"model":123,"choices":[]}`, ""},
		{"duplicate", `{"model":"one","choices":[],"mo\u0064el":"two"}`, ""},
		{"error", `{"model":"FW-Kimi-K3","choices":[],"error":{"message":"failure"}}`, ""},
		{"other protocol", `{"model":"FW-Kimi-K3","output":[]}`, ""},
		{"array", `[{"model":"FW-Kimi-K3","choices":[]}]`, ""},
		{"invalid", `{"model":"FW-Kimi-K3","choices":[]`, ""},
		{"trailing", `{"model":"FW-Kimi-K3","choices":[]}garbage`, ""},
		{"heartbeat", ": PING", ""},
		{"done", "[DONE]", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			assert.Equal(t, tc.input, common.RewriteChatResponseModel(c, tc.input), "disabled")
			common.SetChatResponseModelAlias(c, "kimi-k3")
			want := tc.want
			if want == "" {
				want = tc.input
			}
			assert.Equal(t, want, common.RewriteChatResponseModel(c, tc.input))
			assert.Equal(t, want, string(common.RewriteChatResponseModelBytes(c, []byte(tc.input))))
		})
	}
}

func TestChatResponseModelWritesPreserveDelivery(t *testing.T) {
	input := `{"model":"FW-Kimi-K3","choices":[{"delta":{"content":"hello"}}]}`
	want := `{"model":"kimi-k3","choices":[{"delta":{"content":"hello"}}]}`
	for _, stream := range []bool{false, true} {
		t.Run(strconv.FormatBool(stream), func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			ctx, budget := common.NewRouterResponseBudget(context.Background(), time.Minute)
			defer budget.Close()
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
			common.SetChatResponseModelAlias(c, "kimi-k3")
			if stream {
				require.NoError(t, helper.StringData(c, input))
				assert.Equal(t, "data: "+want+"\n\n", w.Body.String())
			} else {
				require.NoError(t, service.IOCopyBytesChecked(c, nil, []byte(input)))
				assert.Equal(t, want, w.Body.String())
				assert.Equal(t, strconv.Itoa(len(want)), w.Header().Get("Content-Length"))
			}
			assert.True(t, w.Flushed)
			assert.Nil(t, budget.APIError())
		})
	}
}

func TestChatResponseModelPreservesHTTPErrorBody(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetChatResponseModelAlias(c, "kimi-k3")
	body := []byte(`{"model":"FW-Kimi-K3","choices":[],"message":"provider failed"}`)
	require.NoError(t, service.IOCopyBytesChecked(c, &http.Response{StatusCode: 503, Header: http.Header{}}, body))
	assert.Equal(t, 503, w.Code)
	assert.Equal(t, body, w.Body.Bytes())
}

type failingModelWriter struct{ gin.ResponseWriter }

func (w failingModelWriter) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }

func TestChatResponseModelDoesNotHideWriteFailureOrCancellation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, failure := range []string{"write", "cancel", "timeout"} {
			t.Run(strconv.FormatBool(stream)+"/"+failure, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					parent, cancel := context.WithCancel(context.Background())
					defer cancel()
					ctx, budget := common.NewRouterResponseBudget(parent, time.Second)
					defer budget.Close()
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
					common.SetChatResponseModelAlias(c, "kimi-k3")
					switch failure {
					case "write":
						c.Writer = failingModelWriter{c.Writer}
					case "cancel":
						cancel()
						synctest.Wait()
					case "timeout":
						time.Sleep(2 * time.Second)
						synctest.Wait()
					}
					input := `{"model":"FW-Kimi-K3","choices":[{"delta":{"content":"hello"}}]}`
					var err error
					if stream {
						err = helper.StringData(c, input)
					} else {
						err = service.IOCopyBytesChecked(c, nil, []byte(input))
					}
					require.Error(t, err)
					assert.Empty(t, w.Body.String())
					require.NotNil(t, budget.APIError())
					if failure == "timeout" {
						assert.Equal(t, 504, budget.APIError().StatusCode)
					}
				})
			})
		}
	}
}

func BenchmarkChatResponseModel(b *testing.B) {
	for _, tc := range []struct {
		name string
		size int
	}{{"SSE", 128}, {"JSON", 32768}} {
		input := `{"model":"FW-Kimi-K3","choices":[{"delta":{"content":"` + strings.Repeat("a", tc.size) + `"}}],"usage":{"total_tokens":12}}`
		for _, enabled := range []bool{false, true} {
			b.Run(tc.name+"/enabled="+strconv.FormatBool(enabled), func(b *testing.B) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				if enabled {
					common.SetChatResponseModelAlias(c, "kimi-k3")
				}
				data := []byte(input)
				b.ReportAllocs()
				b.ResetTimer()
				if tc.name == "JSON" {
					for b.Loop() {
						_ = common.RewriteChatResponseModelBytes(c, data)
					}
					return
				}
				for b.Loop() {
					_ = common.RewriteChatResponseModel(c, input)
				}
			})
		}
	}
}
