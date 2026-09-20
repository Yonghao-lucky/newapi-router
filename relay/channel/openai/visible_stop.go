package openai

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// visibleStopChoice owns only visible content. Reasoning and tool arguments are
// deliberately kept outside this matcher, including while draining after a hit.
type visibleStopChoice struct {
	pending  string
	matched  bool
	finished bool
}

func (c *visibleStopChoice) consume(content string, stops []string, final bool) string {
	if c.matched {
		return ""
	}
	text := c.pending + content
	c.pending = ""
	first := -1
	for _, stop := range stops {
		if at := strings.Index(text, stop); at >= 0 && (first < 0 || at < first) {
			first = at
		}
	}
	// JSON strings contain complete UTF-8 characters. A suffix matching the
	// beginning of a valid stop string consequently starts at a rune boundary.
	keep := 0
	for _, stop := range stops {
		for n := min(len(text), len(stop)-1); n > keep; n-- {
			if strings.HasSuffix(text, stop[:n]) {
				keep = n
				break
			}
		}
	}
	// A longer stop may start before a complete shorter match in this chunk.
	// Retain that prefix until it resolves so chunk boundaries cannot change
	// which stop wins (e.g. stops "abcd" and "b", chunks "ab" + "cd").
	if first >= 0 && (final || len(text)-keep >= first) {
		c.matched = true
		return text[:first]
	}
	if final {
		return text
	}
	c.pending = text[len(text)-keep:]
	return text[:len(text)-keep]
}

// truncateVisibleResponse changes only message.content and a genuinely matched
// choice's finish_reason. RawMessage retains vendor extensions and usage values.
func truncateVisibleResponse(body []byte, stops []string) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := common.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	var choices []map[string]json.RawMessage
	if err := common.Unmarshal(envelope["choices"], &choices); err != nil {
		return nil, err
	}
	changed := false
	for _, choice := range choices {
		var message map[string]json.RawMessage
		if err := common.Unmarshal(choice["message"], &message); err != nil {
			return nil, err
		}
		if common.GetJsonType(message["content"]) != "string" {
			continue
		}
		var content string
		if err := common.Unmarshal(message["content"], &content); err != nil {
			return nil, err
		}
		state := &visibleStopChoice{}
		content = state.consume(content, stops, true)
		if !state.matched {
			continue
		}
		message["content"], _ = common.Marshal(content)
		choice["message"], _ = common.Marshal(message)
		choice["finish_reason"] = json.RawMessage(`"stop"`)
		changed = true
	}
	if !changed {
		return body, nil
	}
	envelope["choices"], _ = common.Marshal(choices)
	return common.Marshal(envelope)
}

type visibleStopStream struct {
	stops          []string
	choices        map[int]*visibleStopChoice
	lastEnvelope   map[string]json.RawMessage
	lastHasChoices bool
}

func newVisibleStopStream(stops []string) *visibleStopStream {
	return &visibleStopStream{stops: stops, choices: make(map[int]*visibleStopChoice)}
}

func (s *visibleStopStream) process(data string) (string, error) {
	var envelope map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(data, &envelope); err != nil {
		return "", err
	}
	var choices []map[string]json.RawMessage
	if raw, ok := envelope["choices"]; ok {
		if err := common.Unmarshal(raw, &choices); err != nil {
			return "", err
		}
	}
	s.lastEnvelope = envelope
	s.lastHasChoices = len(choices) > 0
	filtered := make([]map[string]json.RawMessage, 0, len(choices))
	for _, choice := range choices {
		var index int
		if err := common.Unmarshal(choice["index"], &index); err != nil {
			return "", err
		}
		state := s.choices[index]
		if state == nil {
			state = &visibleStopChoice{}
			s.choices[index] = state
		}
		if state.finished {
			continue
		}
		var delta map[string]json.RawMessage
		if raw, ok := choice["delta"]; ok {
			if err := common.Unmarshal(raw, &delta); err != nil {
				return "", err
			}
		}
		var finish string
		if raw, ok := choice["finish_reason"]; ok {
			if err := common.Unmarshal(raw, &finish); err != nil {
				return "", err
			}
		}
		final := finish != ""
		hasContent := common.GetJsonType(delta["content"]) == "string"
		var content string
		if hasContent {
			if err := common.Unmarshal(delta["content"], &content); err != nil {
				return "", err
			}
		}
		if hasContent || (final && state.pending != "") {
			content = state.consume(content, s.stops, final)
			if delta == nil {
				delta = make(map[string]json.RawMessage)
			}
			delta["content"], _ = common.Marshal(content)
			choice["delta"], _ = common.Marshal(delta)
		}
		if final {
			if state.matched {
				choice["finish_reason"] = json.RawMessage(`"stop"`)
			}
			state.finished = true
		}
		filtered = append(filtered, choice)
	}
	if len(choices) > 0 {
		if len(filtered) == 0 && len(envelope["usage"]) == 0 {
			return "", nil
		}
		envelope["choices"], _ = common.Marshal(filtered)
	}
	body, err := common.Marshal(envelope)
	return string(body), err
}

// flush resolves a pending prefix at a normal upstream end. It only supplies a
// finish reason for an actual stop match when the upstream omitted its finish.
func (s *visibleStopStream) flush() (string, error) {
	indices := make([]int, 0, len(s.choices))
	for index, state := range s.choices {
		if !state.finished && (state.pending != "" || state.matched) {
			indices = append(indices, index)
		}
	}
	if len(indices) == 0 {
		return "", nil
	}
	slices.Sort(indices)
	choices := make([]map[string]any, 0, len(indices))
	for _, index := range indices {
		state := s.choices[index]
		content := state.consume("", s.stops, true)
		var finish any
		if state.matched {
			finish = "stop"
			state.finished = true
		}
		choices = append(choices, map[string]any{
			"index": index, "delta": map[string]string{"content": content}, "finish_reason": finish,
		})
	}
	envelope := make(map[string]json.RawMessage)
	for _, key := range []string{"id", "object", "created", "model", "system_fingerprint", "service_tier"} {
		if raw, ok := s.lastEnvelope[key]; ok {
			envelope[key] = raw
		}
	}
	envelope["choices"], _ = common.Marshal(choices)
	body, err := common.Marshal(envelope)
	return string(body), err
}
