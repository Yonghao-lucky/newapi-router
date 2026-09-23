package common

import (
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const chatResponseModelAliasKey = "chat_response_model_alias"

// This is a response-only request snapshot. It never changes provider, routing
// or billing model identities. Each channel attempt replaces this value before
// sending its upstream request, including clearing the previous attempt's rule.
type chatResponseModelAlias struct {
	name   string
	quoted string
}

func SetChatResponseModelAlias(c *gin.Context, model string) {
	if c == nil {
		return
	}
	if model == "" {
		c.Set(chatResponseModelAliasKey, nil)
		return
	}
	quoted, err := Marshal(model)
	if err != nil {
		return
	}
	c.Set(chatResponseModelAliasKey, chatResponseModelAlias{model, string(quoted)})
}

func RewriteChatResponseModel(c *gin.Context, data string) string {
	if c == nil {
		return data
	}
	value, enabled := c.Get(chatResponseModelAliasKey)
	if !enabled {
		return data
	}
	alias, ok := value.(chatResponseModelAlias)
	if !ok {
		return data
	}
	return alias.rewrite(data)
}

func RewriteChatResponseModelBytes(c *gin.Context, data []byte) []byte {
	if c == nil {
		return data
	}
	value, enabled := c.Get(chatResponseModelAliasKey)
	if !enabled {
		return data
	}
	alias, ok := value.(chatResponseModelAlias)
	if !ok {
		return data
	}
	original := string(data)
	rewritten := alias.rewrite(original)
	if rewritten == original {
		return data
	}
	return []byte(rewritten)
}

func (alias chatResponseModelAlias) rewrite(data string) string {
	// Validate before using token offsets. Unknown/invalid upstream envelopes
	// remain the existing handler's responsibility, not a new relay failure.
	if !gjson.Valid(data) {
		return data
	}
	root := gjson.Parse(data)
	if !root.IsObject() {
		return data
	}
	var model gjson.Result
	var modelCount int
	var hasChoices, hasError bool
	root.ForEach(func(key, value gjson.Result) bool {
		switch key.Str {
		case "model":
			modelCount++
			model = value
		case "choices":
			hasChoices = value.IsArray()
		case "error":
			hasError = true
		}
		return true
	})
	if hasError || !hasChoices || modelCount != 1 || model.Type != gjson.String || model.Str == alias.name {
		return data
	}
	// Replace exactly the one string token. Do not reserialize the envelope:
	// that would alter unknown extensions, number precision or usage fields.
	return data[:model.Index] + alias.quoted + data[model.Index+len(model.Raw):]
}
