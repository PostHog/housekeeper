package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// fakeConverser replays scripted model turns and records each request.
type fakeConverser struct {
	turns    []*bedrockruntime.ConverseOutput
	requests [][]types.Message
	onCall   func(call int)
}

func (f *fakeConverser) Converse(_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	call := len(f.requests)
	f.requests = append(f.requests, append([]types.Message(nil), in.Messages...))
	if f.onCall != nil {
		f.onCall(call)
	}
	if call >= len(f.turns) {
		return nil, errors.New("unexpected extra Converse call")
	}
	return f.turns[call], nil
}

func textTurn(text string) *bedrockruntime.ConverseOutput {
	return &bedrockruntime.ConverseOutput{
		StopReason: types.StopReasonEndTurn,
		Output: &types.ConverseOutputMemberMessage{Value: types.Message{
			Role:    types.ConversationRoleAssistant,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: text}},
		}},
	}
}

func toolTurn(id, text string) *bedrockruntime.ConverseOutput {
	content := []types.ContentBlock{}
	if text != "" {
		content = append(content, &types.ContentBlockMemberText{Value: text})
	}
	content = append(content, &types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{
		ToolUseId: aws.String(id),
		Name:      aws.String("run_sql"),
		Input:     brdoc.NewLazyDocument(map[string]any{"sql": "SELECT 1"}),
	}})
	return &bedrockruntime.ConverseOutput{
		StopReason: types.StopReasonToolUse,
		Output: &types.ConverseOutputMemberMessage{Value: types.Message{
			Role:    types.ConversationRoleAssistant,
			Content: content,
		}},
	}
}

// lastUserMessage renders the text of the final message in a request: tool
// results and plain text blocks.
func lastUserMessage(msgs []types.Message) (texts []string, statuses []types.ToolResultStatus) {
	last := msgs[len(msgs)-1]
	for _, block := range last.Content {
		switch b := block.(type) {
		case *types.ContentBlockMemberText:
			texts = append(texts, b.Value)
		case *types.ContentBlockMemberToolResult:
			statuses = append(statuses, b.Value.Status)
			for _, c := range b.Value.Content {
				if tc, ok := c.(*types.ToolResultContentBlockMemberText); ok {
					texts = append(texts, tc.Value)
				}
			}
		}
	}
	return texts, statuses
}

func runFakeAgent(t *testing.T, f *fakeConverser, handle toolHandler, maxIterations, maxSeconds int32) (string, error) {
	t.Helper()
	tools := []bedrockTool{{name: "run_sql", description: "d", inputSchema: map[string]any{"type": "object"}}}
	return runBedrockAgent(context.Background(), f, "model", "system", "question", tools, handle, 256, maxIterations, maxSeconds, -1, nil)
}

func countingHandler(calls *int, err error) toolHandler {
	return func(string, map[string]any) (string, error) {
		*calls++
		if err != nil {
			return "", err
		}
		return "1 rows", nil
	}
}

func TestBedrockAgentAnswers(t *testing.T) {
	var calls int
	f := &fakeConverser{turns: []*bedrockruntime.ConverseOutput{toolTurn("t1", ""), textTurn("diagnosis")}}
	got, err := runFakeAgent(t, f, countingHandler(&calls, nil), 8, 0)
	if err != nil || got != "diagnosis" {
		t.Fatalf("got (%q, %v), want (diagnosis, nil)", got, err)
	}
	if calls != 1 || len(f.requests) != 2 {
		t.Errorf("tool calls = %d, converse calls = %d, want 1 and 2", calls, len(f.requests))
	}
}

func TestBedrockAgentToolErrorGoesToModel(t *testing.T) {
	var calls int
	f := &fakeConverser{turns: []*bedrockruntime.ConverseOutput{toolTurn("t1", ""), textTurn("recovered")}}
	got, err := runFakeAgent(t, f, countingHandler(&calls, errors.New("boom")), 8, 0)
	if err != nil || got != "recovered" {
		t.Fatalf("got (%q, %v), want (recovered, nil)", got, err)
	}
	texts, statuses := lastUserMessage(f.requests[1])
	if len(statuses) != 1 || statuses[0] != types.ToolResultStatusError || texts[0] != "error: boom" {
		t.Errorf("tool result = %v %v, want one error result \"error: boom\"", statuses, texts)
	}
}

func TestBedrockAgentFinalTurnRefusesTools(t *testing.T) {
	var calls int
	f := &fakeConverser{turns: []*bedrockruntime.ConverseOutput{
		toolTurn("t1", ""), toolTurn("t2", ""), textTurn("summary"),
	}}
	got, err := runFakeAgent(t, f, countingHandler(&calls, nil), 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "summary") || !strings.Contains(got, "iteration budget") {
		t.Errorf("answer = %q, want the summary with the iteration note", got)
	}
	if calls != 2 {
		t.Errorf("tool calls = %d, want 2", calls)
	}
	texts, _ := lastUserMessage(f.requests[2])
	if texts[len(texts)-1] != iterationNudge {
		t.Errorf("final-turn nudge = %q, want %q", texts[len(texts)-1], iterationNudge)
	}
}

func TestBedrockAgentFinalTurnStillAskingForTools(t *testing.T) {
	var calls int
	f := &fakeConverser{turns: []*bedrockruntime.ConverseOutput{
		toolTurn("t1", "partial finding"), toolTurn("t2", ""),
	}}
	got, err := runFakeAgent(t, f, countingHandler(&calls, nil), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "partial finding") {
		t.Errorf("answer = %q, want the last text the model gave", got)
	}
	if calls != 1 || len(f.requests) != 2 {
		t.Errorf("tool calls = %d, converse calls = %d, want 1 and 2", calls, len(f.requests))
	}

	f = &fakeConverser{turns: []*bedrockruntime.ConverseOutput{toolTurn("t1", ""), toolTurn("t2", "")}}
	if _, err := runFakeAgent(t, f, countingHandler(&calls, nil), 1, 0); err == nil {
		t.Error("want an error when the model never answers")
	}
}

func TestBedrockAgentBudgetRefusesTools(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	bedrockNow = func() time.Time { return now }
	t.Cleanup(func() { bedrockNow = time.Now })

	var calls int
	f := &fakeConverser{
		turns: []*bedrockruntime.ConverseOutput{toolTurn("t1", ""), toolTurn("t2", ""), textTurn("summary")},
		// The second model turn returns after the 30s budget.
		onCall: func(call int) {
			if call == 1 {
				now = now.Add(time.Minute)
			}
		},
	}
	got, err := runFakeAgent(t, f, countingHandler(&calls, nil), 8, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "summary") || !strings.Contains(got, "time budget") {
		t.Errorf("answer = %q, want the summary with the time-budget note", got)
	}
	if calls != 1 {
		t.Errorf("tool calls = %d, want 1 (the over-budget call is refused)", calls)
	}
	texts, statuses := lastUserMessage(f.requests[2])
	if len(statuses) != 1 || statuses[0] != types.ToolResultStatusError || texts[0] != budgetRefusal || texts[1] != budgetNudge {
		t.Errorf("over-budget results = %v %v, want a refusal and the nudge", statuses, texts)
	}
}
