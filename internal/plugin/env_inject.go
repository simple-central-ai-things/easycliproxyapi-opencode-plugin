package plugin

import (
	"bytes"
	"encoding/json"

	"opencode-go-cliproxyapi/internal/catalog"
)

// opencodeEnvBlock is the OpenCode environment-context block the opencode.ai
// inference plane carries on every genuine OpenCode session. The free-tier gate
// keys on this block's presence (the field labels), not on specific values — a
// synthetic block with the standard labels is accepted, a request without it is
// rejected as "FreeUsageLimitError: Free models work in OpenCode only".
const opencodeEnvBlock = "<env>\n" +
	"  Working directory: /app\n" +
	"  Workspace root folder: /app\n" +
	"  Is directory a git repo: no\n" +
	"  Platform: linux\n" +
	"</env>"

// injectOpencodeEnv prepends the OpenCode environment-context block to the
// upstream body's system content, per route. It is idempotent: a body that
// already carries an <env> block (a genuine OpenCode client, or a forwarded
// session) is returned unchanged. Injection is best-effort — any decode or
// encode failure returns the body untouched so it never breaks a request that
// would otherwise reach upstream.
func injectOpencodeEnv(route catalog.Route, body []byte) []byte {
	if bytes.Contains(body, []byte("<env>")) {
		return body
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body
	}
	switch route {
	case catalog.RouteChatCompletions:
		injectIntoMessagesArray(doc, "messages")
	case catalog.RouteMessages:
		injectIntoAnthropicSystem(doc)
	case catalog.RouteResponses:
		injectIntoMessagesArray(doc, "input")
	default:
		return body
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// injectIntoMessagesArray prepends the env block to the first system/developer
// message in a JSON-array field (chat-completions "messages", responses
// "input"), inserting a synthetic system message when none exists.
func injectIntoMessagesArray(doc map[string]any, field string) {
	msgs, _ := doc[field].([]any)
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := mm["role"].(string); role == "system" || role == "developer" {
			content, _ := mm["content"].(string)
			mm["content"] = opencodeEnvBlock + "\n\n" + content
			return
		}
	}
	doc[field] = append([]any{map[string]any{"role": "system", "content": opencodeEnvBlock}}, msgs...)
}

// injectIntoAnthropicSystem appends the env block to the Anthropic top-level
// system field (string or block array), creating it when absent.
func injectIntoAnthropicSystem(doc map[string]any) {
	switch sys := doc["system"].(type) {
	case string:
		doc["system"] = opencodeEnvBlock + "\n\n" + sys
	case []any:
		doc["system"] = append(sys, map[string]any{"type": "text", "text": opencodeEnvBlock})
	default:
		doc["system"] = opencodeEnvBlock
	}
}
