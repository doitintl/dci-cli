package main

import (
	"reflect"
	"testing"
)

// A trimmed capture of a real build-cloud-flow event stream (2026-08-26):
// header event, tool lifecycle, token-by-token answer text, and the
// cloudflow_created custom event.
const builderStreamFixture = "data: {\"answerId\":\"9Cvpb58zMtQfLUzC5JAQ\",\"conversationId\":\"0uTcI08iexsmKLlx73qM\"}\n\n" +
	"data: {\"answer\":\"{\\\"toolStart\\\":\\\"Creating a new CloudFlow\\\",\\\"toolId\\\":\\\"t1\\\",\\\"toolStatus\\\":\\\"running\\\",\\\"input\\\":\\\"{}\\\"}\"}\n\n" +
	"data: {\"answer\":\"{\\\"toolEnd\\\":\\\"Creating a new CloudFlow\\\",\\\"toolId\\\":\\\"t1\\\",\\\"toolStatus\\\":\\\"completed\\\"}\"}\n\n" +
	"data: {\"answer\":\"{\\\"customEvent\\\":{\\\"messageId\\\":\\\"cloudflow_created\\\",\\\"data\\\":{\\\"flowId\\\":\\\"wulTlJNxkGwTpe7T2DxT\\\"}}}\"}\n\n" +
	"data: {\"answer\":\"{\\\"toolStart\\\":\\\"Getting operation input parameters\\\",\\\"toolId\\\":\\\"t2\\\",\\\"toolStatus\\\":\\\"running\\\",\\\"input\\\":\\\"{\\\\\\\"provider\\\\\\\":\\\\\\\"DoiT\\\\\\\"}\\\"}\"}\n\n" +
	"data: {\"answer\":\"{\\\"llmStart\\\":\\\"ChatOpenAI\\\",\\\"value\\\":\\\"Thinking\\\"}\"}\n\n" +
	"data: {\"answer\":\"Which\"}\n\n" +
	"data: {\"answer\":\" Data\"}\n\n" +
	"data: {\"answer\":\"Hub\"}\n\n" +
	"data: {\"answer\":\" dataset\"}\n\n" +
	"data: {\"answer\":\"?\"}\n\n" +
	"data: {\"answer\":\"{\\\"llmEnd\\\":\\\"ChatOpenAI\\\"}\"}\n\n"

func TestTransformCloudflowBuildStream(t *testing.T) {
	t.Cleanup(func() { invokedCommandName = "" })
	invokedCommandName = "build-cloud-flow"

	result, ok := transformCloudflowBuildStream(builderStreamFixture).(map[string]interface{})
	if !ok {
		t.Fatal("expected a structured result map")
	}
	if result["conversationId"] != "0uTcI08iexsmKLlx73qM" {
		t.Errorf("conversationId = %v", result["conversationId"])
	}
	if result["flowId"] != "wulTlJNxkGwTpe7T2DxT" {
		t.Errorf("flowId = %v", result["flowId"])
	}
	if result["answer"] != "Which DataHub dataset?" {
		t.Errorf("answer = %q", result["answer"])
	}
	wantSteps := []string{"Creating a new CloudFlow", "Getting operation input parameters"}
	if !reflect.DeepEqual(result["steps"], wantSteps) {
		t.Errorf("steps = %v, want %v", result["steps"], wantSteps)
	}
}

func TestTransformCloudflowBuildStreamPassthrough(t *testing.T) {
	t.Cleanup(func() { invokedCommandName = "" })

	// Other commands: untouched even if the body looks like a stream.
	invokedCommandName = "list-cloudflows"
	if got := transformCloudflowBuildStream(builderStreamFixture); got != builderStreamFixture {
		t.Error("non-builder command body was transformed")
	}

	// Builder command, but not an SSE body: untouched.
	invokedCommandName = "refine-cloud-flow"
	if got := transformCloudflowBuildStream("plain error text"); got != "plain error text" {
		t.Error("non-stream string body was transformed")
	}
	structured := map[string]interface{}{"already": "parsed"}
	if got := transformCloudflowBuildStream(structured); !reflect.DeepEqual(got, structured) {
		t.Error("non-string body was transformed")
	}

	// A stream with data: lines that carry none of the expected fields:
	// untouched rather than replaced with an empty object.
	if got := transformCloudflowBuildStream("data: {\"other\":1}\n\n"); got != "data: {\"other\":1}\n\n" {
		t.Error("unrecognized stream was replaced")
	}
}

func TestParseBuilderLifecycleEventKeepsJSONLookingText(t *testing.T) {
	if parseBuilderLifecycleEvent("{\"not\":\"lifecycle\"}") != nil {
		t.Error("plain JSON text token was classified as a lifecycle event")
	}
	if parseBuilderLifecycleEvent("plain text") != nil {
		t.Error("plain text token was classified as a lifecycle event")
	}
	event := parseBuilderLifecycleEvent("{\"toolStart\":\"X\"}")
	if event == nil || event["toolStart"] != "X" {
		t.Errorf("lifecycle event not recognized: %v", event)
	}
}
