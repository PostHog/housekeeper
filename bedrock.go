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
	timedOut := false
	for i := int32(0); i < maxIterations; i++ {
		convInput := &bedrockruntime.ConverseInput{
			ModelId:  aws.String(modelID),
			System:   []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: system}},
			Messages: messages,
			InferenceConfig: &types.InferenceConfiguration{
				MaxTokens:   aws.Int32(maxTokens),
				Temperature: aws.Float32(temperature),
			},
		}
		// ToolConfig must always be set — Bedrock rejects a request whose history
		// contains tool blocks if it's omitted. When the wall-clock budget is
		// exceeded we instead nudge the model (below) to stop and summarize.
		convInput.ToolConfig = toolCfg
		overBudget := maxSeconds > 0 && !time.Now().Before(deadline)
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
		messages = append(messages, assistant)

		// Collect any text and any tool-use requests from this turn.
		var toolResults []types.ContentBlock
		for _, block := range assistant.Content {
			switch b := block.(type) {
			case *types.ContentBlockMemberText:
				finalText = b.Value
			case *types.ContentBlockMemberToolUse:
				tu := b.Value
				name := aws.ToString(tu.Name)
				var input map[string]any
				if tu.Input != nil {
					if err := tu.Input.UnmarshalSmithyDocument(&input); err != nil {
						logrus.WithError(err).Warn("diagnose: failed to decode tool input")
					}
				}
				logrus.WithFields(logrus.Fields{"tool": name, "iter": i}).Debug("diagnose: tool call")
				result, herr := handle(name, input)
				trb := types.ToolResultBlock{ToolUseId: tu.ToolUseId}
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

		// Feed tool results back. If we're over the time budget, append a nudge so
		// the model stops investigating and summarizes on its next turn (tools stay
		// available — we can't withhold them once history has tool blocks).
		if overBudget {
			timedOut = true
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
