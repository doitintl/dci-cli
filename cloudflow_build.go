// CloudFlow NL-builder stream handling: build-cloud-flow and
// refine-cloud-flow answer with a text/event-stream of build events — tool
// lifecycle markers, token-by-token assistant text, and custom events carrying
// the created flow's ID. Raw, that stream reaches the output formatter as one
// quoted string: unreadable for humans and unparseable-without-work for
// agents. This chapter parses it into the result the caller actually needs:
// the flow ID, the conversation ID (to continue with refine-cloud-flow), the
// assistant's answer text, and the build steps that ran.
package main

import (
	"encoding/json"
	"strings"
)

var cloudflowBuilderOperations = map[string]bool{
	"build-cloud-flow":  true,
	"refine-cloud-flow": true,
}

// transformCloudflowBuildStream parses the NL flow builder's SSE body into a
// structured result. Anything that isn't recognizably that stream — another
// command, a non-string body, no data: lines — passes through untouched, so a
// server-side format change degrades to today's raw output rather than an
// empty one.
func transformCloudflowBuildStream(body interface{}) interface{} {
	if !cloudflowBuilderOperations[invokedCommandName] {
		return body
	}
	stream, ok := body.(string)
	if !ok || !strings.Contains(stream, "data: ") {
		return body
	}

	var answer strings.Builder
	var steps []string
	result := map[string]interface{}{}

	for _, line := range strings.Split(stream, "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok {
			continue
		}
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if id, ok := event["conversationId"].(string); ok && id != "" {
			result["conversationId"] = id
		}
		token, ok := event["answer"].(string)
		if !ok {
			continue
		}
		lifecycle := parseBuilderLifecycleEvent(token)
		if lifecycle == nil {
			answer.WriteString(token)
			continue
		}
		if step, ok := lifecycle["toolStart"].(string); ok && step != "" {
			steps = append(steps, step)
		}
		if flowID := builderCreatedFlowID(lifecycle); flowID != "" {
			result["flowId"] = flowID
		}
	}

	text := strings.TrimSpace(answer.String())
	if text == "" && result["conversationId"] == nil && result["flowId"] == nil {
		return body
	}
	if text != "" {
		result["answer"] = text
	}
	if len(steps) > 0 {
		result["steps"] = steps
	}
	return result
}

// parseBuilderLifecycleEvent distinguishes the stream's embedded lifecycle
// JSON (toolStart/toolEnd/llmStart/llmEnd/customEvent) from assistant text.
// A text token that merely looks like JSON stays text unless it carries one
// of the lifecycle keys.
func parseBuilderLifecycleEvent(token string) map[string]interface{} {
	if !strings.HasPrefix(token, "{") {
		return nil
	}
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(token), &event); err != nil {
		return nil
	}
	for _, key := range []string{"toolStart", "toolEnd", "llmStart", "llmEnd", "customEvent"} {
		if _, ok := event[key]; ok {
			return event
		}
	}
	return nil
}

func builderCreatedFlowID(lifecycle map[string]interface{}) string {
	custom, ok := lifecycle["customEvent"].(map[string]interface{})
	if !ok {
		return ""
	}
	if custom["messageId"] != "cloudflow_created" {
		return ""
	}
	data, ok := custom["data"].(map[string]interface{})
	if !ok {
		return ""
	}
	flowID, _ := data["flowId"].(string)
	return flowID
}
