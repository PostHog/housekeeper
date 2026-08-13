package main

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// bedrockEnabled reports whether the in-account Bedrock diagnose path is
// configured. Both a region and a model id (an inference-profile id such as
// "us.anthropic.claude-sonnet-4-6-...") are required.
func bedrockEnabled() bool {
	return viper.GetString("bedrock.region") != "" && viper.GetString("bedrock.model_id") != ""
}

// newBedrockClient builds a bedrockruntime client. Credentials come from the
// default AWS chain — in-cluster this is the pod's IRSA web-identity role, so
// no static keys are ever configured.
func newBedrockClient(ctx context.Context) (*bedrockruntime.Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(viper.GetString("bedrock.region")))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return bedrockruntime.NewFromConfig(cfg), nil
}

// bedrockTool describes a tool exposed to the model during a Converse loop.
type bedrockTool struct {
	name        string
	description string
	inputSchema map[string]any
}

// toolHandler executes a tool the model asked for and returns a text result.
// An error is surfaced back to the model as a tool error (it can recover),
// not returned to the caller — only a fatal transport error aborts the loop.
type toolHandler func(name string, input map[string]any) (string, error)

// progressFunc reports investigation progress (iteration counter + message).
// Nil disables reporting. Used to emit MCP progress notifications so
// spec-compliant clients reset their tool timeout during long investigations.
type progressFunc func(iteration int32, message string)

// runBedrockAgent drives a Converse tool-use loop until the model produces a
// final answer or a limit is hit (iteration count or wall-clock budget). When
// the time budget is exceeded it makes one final tool-free turn so the model
// summarizes what it found rather than timing out the caller. Returns that text.
func runBedrockAgent(
	ctx context.Context,
	client *bedrockruntime.Client,
	modelID, system, userMsg string,
	tools []bedrockTool,
	handle toolHandler,
	maxTokens, maxIterations, maxSeconds int32,
	temperature float32,
	progress progressFunc,
) (string, error) {
	toolCfg := &types.ToolConfiguration{Tools: make([]types.Tool, 0, len(tools))}
	for _, t := range tools {
		toolCfg.Tools = append(toolCfg.Tools, &types.ToolMemberToolSpec{
			Value: types.ToolSpecification{
				Name:        aws.String(t.name),
				Description: aws.String(t.description),
				InputSchema: &types.ToolInputSchemaMemberJson{
					Value: brdoc.NewLazyDocument(t.inputSchema),
				},
			},
		})
	}

	messages := []types.Message{{
		Role:    types.ConversationRoleUser,
		Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: userMsg}},
	}}

	var finalText string
	var inTok, outTok int32 // accumulated Bedrock token usage across iterations
	var deadline time.Time
	if maxSeconds > 0 {
		deadline = time.Now().Add(time.Duration(maxSeconds) * time.Second)
	}
	// temperature < 0 means "don't send" — newer Anthropic models (Sonnet 5+)
	// reject the temperature parameter on Converse entirely, so it's opt-in for
	// the older models that still accept it.
	infCfg := &types.InferenceConfiguration{MaxTokens: aws.Int32(maxTokens)}
	if temperature >= 0 {
		infCfg.Temperature = aws.Float32(temperature)
	}
	timedOut := false
	for i := int32(0); i < maxIterations; i++ {
		// One tick per model turn (~15-40s apart) — frequent enough that a
		// spec-compliant MCP client resets its tool timeout between turns.
		if progress != nil {
			progress(i+1, fmt.Sprintf("investigating: model turn %d/%d", i+1, maxIterations))
		}
		convInput := &bedrockruntime.ConverseInput{
			ModelId:         aws.String(modelID),
			System:          []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: system}},
			Messages:        messages,
			InferenceConfig: infCfg,
		}
		// ToolConfig must always be set — Bedrock rejects a request whose history
		// contains tool blocks if it's omitted. When the wall-clock budget is
		// exceeded we instead refuse tool execution (below) so the model summarizes.
		convInput.ToolConfig = toolCfg
		out, err := client.Converse(ctx, convInput)
		if err != nil {
			return "", fmt.Errorf("bedrock converse: %w", err)
		}
		if out.Usage != nil {
			inTok += aws.ToInt32(out.Usage.InputTokens)
			outTok += aws.ToInt32(out.Usage.OutputTokens)
		}

		msgOut, ok := out.Output.(*types.ConverseOutputMemberMessage)
		if !ok {
			return "", fmt.Errorf("unexpected converse output type %T", out.Output)
		}
		assistant := msgOut.Value
		// Echo the assistant turn back into the history so tool results line up.
		// Strip content blocks this SDK build can't re-serialize (decoded as
		// UnknownUnionMember, e.g. new block types from newer models): echoing
		// one back fails the next Converse call outright. Dropping it degrades
		// gracefully; the durable fix is keeping the bedrockruntime SDK current.
		echo := assistant
		echo.Content = make([]types.ContentBlock, 0, len(assistant.Content))
		for _, block := range assistant.Content {
			if _, unknown := block.(*types.UnknownUnionMember); unknown {
				logrus.Warn("diagnose: dropping unrecognized assistant content block (bedrockruntime SDK likely older than the model)")
				continue
			}
			echo.Content = append(echo.Content, block)
		}
		messages = append(messages, echo)

		// Collect any text and any tool-use requests from this turn. The budget is
		// re-checked HERE, after the turn returns, so a deadline that expired while
		// the model was generating (or while earlier tools ran) refuses the newly
		// requested tools instead of executing them — the previous shape executed
		// one more full round of tools and only nudged afterwards, overshooting the
		// budget by up to two model turns plus their queries.
		overBudget := maxSeconds > 0 && !time.Now().Before(deadline)
		var toolResults []types.ContentBlock
		for _, block := range assistant.Content {
			switch b := block.(type) {
			case *types.ContentBlockMemberText:
				finalText = b.Value
			case *types.ContentBlockMemberToolUse:
				tu := b.Value
				trb := types.ToolResultBlock{ToolUseId: tu.ToolUseId}
				if overBudget {
					// Refuse execution: every pending call gets a budget error so
					// the model's only productive next move is the final summary.
					timedOut = true
					trb.Status = types.ToolResultStatusError
					trb.Content = []types.ToolResultContentBlock{
						&types.ToolResultContentBlockMemberText{Value: "error: time budget exceeded — do not call any more tools; give your final summary of findings now."},
					}
					toolResults = append(toolResults, &types.ContentBlockMemberToolResult{Value: trb})
					continue
				}
				name := aws.ToString(tu.Name)
				var input map[string]any
				if tu.Input != nil {
					if err := tu.Input.UnmarshalSmithyDocument(&input); err != nil {
						logrus.WithError(err).Warn("diagnose: failed to decode tool input")
					}
				}
				logrus.WithFields(logrus.Fields{"tool": name, "iter": i}).Debug("diagnose: tool call")
				result, herr := handle(name, input)
				if herr != nil {
					trb.Status = types.ToolResultStatusError
					trb.Content = []types.ToolResultContentBlock{
						&types.ToolResultContentBlockMemberText{Value: "error: " + herr.Error()},
					}
				} else {
					trb.Content = []types.ToolResultContentBlock{
						&types.ToolResultContentBlockMemberText{Value: result},
					}
				}
				toolResults = append(toolResults, &types.ContentBlockMemberToolResult{Value: trb})
			}
		}

		if out.StopReason != types.StopReasonToolUse || len(toolResults) == 0 {
			// Model is done (or we withheld tools after the time budget).
			logrus.WithFields(logrus.Fields{
				"iterations": i + 1, "input_tokens": inTok, "output_tokens": outTok, "timed_out": timedOut,
			}).Info("diagnose: complete")
			if timedOut {
				return finalText + "\n\n(note: time budget reached; summary reflects findings so far)", nil
			}
			return finalText, nil
		}

		// Feed tool results back. Over budget, the results themselves are budget
		// errors (above); the extra text block reinforces the stop instruction
		// (tools stay schema-available — Bedrock requires ToolConfig once history
		// has tool blocks).
		if overBudget {
			toolResults = append(toolResults, &types.ContentBlockMemberText{
				Value: "Time budget reached — do not call any more tools; give your final summary of findings now.",
			})
		}
		messages = append(messages, types.Message{
			Role:    types.ConversationRoleUser,
			Content: toolResults,
		})
	}

	logrus.WithFields(logrus.Fields{
		"iterations": maxIterations, "input_tokens": inTok, "output_tokens": outTok,
	}).Info("diagnose: hit iteration budget")
	if finalText != "" {
		return finalText + "\n\n(note: investigation hit the iteration budget; answer may be partial)", nil
	}
	return "", fmt.Errorf("diagnose exhausted %d iterations without a final answer", maxIterations)
}
