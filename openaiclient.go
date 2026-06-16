package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

type completionOptions struct {
	Reasoning     int
	ShowReasoning bool
}

type completionState struct {
	err         error
	reasoning string
	toolInvokes map[int64]*openai.ChatCompletionChunkChoiceDeltaToolCall
	result      string
}

func completionError(err error) completionState {
	return completionState{err: err}
}

func (c *Client) StreamCompletion(
	ctx context.Context,
	messages []openai.ChatCompletionMessageParamUnion,
	options completionOptions,
	output chan<- string,
) ([]openai.ChatCompletionMessageParamUnion, error) {
	for {
		state := c.streamCompletionPart(ctx, messages, options, output)
		if state.err != nil {
			return nil, state.err
		}
		if len(state.toolInvokes) > 0 {
			toolCallsParam := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(state.toolInvokes))
			for _, invoke := range state.toolInvokes {
				toolCallsParam = append(toolCallsParam, openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID: invoke.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name: invoke.Function.Name,
							Arguments: invoke.Function.Arguments,
						},
					},
				})
			}
			messages = append(messages, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					ToolCalls: toolCallsParam,
				},
			})
			for _, invoke := range state.toolInvokes {
				tool, ok := c.tools[invoke.Function.Name]
				if !ok {
					return nil, fmt.Errorf("trying to invoke non-exist tool %q", invoke.Function.Name)
				}
				res, err := tool.Callback(c.ctx, invoke.Function.Arguments)
				if err != nil {
					return nil, fmt.Errorf("tool %q error: %w\n\tinput: %s", invoke.Function.Name, err, invoke.Function.Arguments)
				}
				messages = append(
					messages,
					openai.ChatCompletionMessageParamUnion{
						OfTool: &openai.ChatCompletionToolMessageParam{
							ToolCallID: invoke.ID,
							Content: openai.ChatCompletionToolMessageParamContentUnion{
								OfString: openai.String(res),
							},
						},
					},
				)
			}
			continue
		}
		messages = append(messages, openai.ChatCompletionMessageParamUnion{
			OfAssistant: &openai.ChatCompletionAssistantMessageParam{
				Content: openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(state.result),
				},
			},
		})
		return messages, nil
	}
}

func (c *Client) streamCompletionPart(
	ctx context.Context,
	messages []openai.ChatCompletionMessageParamUnion,
	options completionOptions,
	output chan<- string,
) completionState {
	params := openai.ChatCompletionNewParams{
		Messages: messages,
		FrequencyPenalty: openai.Float(0.2),
		Tools: c.toolParam,
	}
	switch options.Reasoning {
	case 0:
		options.ShowReasoning = false
		params.ReasoningEffort = shared.ReasoningEffortNone
	case 1:
		params.ReasoningEffort = shared.ReasoningEffortMinimal
	case 2:
		params.ReasoningEffort = shared.ReasoningEffortLow
	case 3:
		params.ReasoningEffort = shared.ReasoningEffortMedium
	case 4:
		params.ReasoningEffort = shared.ReasoningEffortHigh
	case 5:
		params.ReasoningEffort = shared.ReasoningEffortXhigh
	}
	stream := c.aiCli.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()

	var (
		resultBuf strings.Builder
		reasoningBuf strings.Builder
		reasoningDone = false
		toolInvokes = make(map[int64]*openai.ChatCompletionChunkChoiceDeltaToolCall)
	)

	for stream.Next() {
		event := stream.Current()
		choice := event.Choices[0]
		switch choice.FinishReason {
		case "stop":
			break
		case "tool_calls":
			return completionState{
				reasoning: reasoningBuf.String(),
				toolInvokes: toolInvokes,
			}
		case "":
		default:
			return completionError(fmt.Errorf("unexpected finish reason: %s", choice.FinishReason))
		}

		delta := choice.Delta

		var reasoningDeltaTmp struct {
			Reasoning string `json:"reasoning"`
		}
		if err := json.Unmarshal(([]byte) (delta.RawJSON()), &reasoningDeltaTmp); err == nil && len(reasoningDeltaTmp.Reasoning) > 0 {
			reasoningBuf.WriteString(reasoningDeltaTmp.Reasoning)
			if options.ShowReasoning {
				if reasoningDone {
					return completionError(errors.New("unexpected reasoning chunk after reasoning is done"))
				}
				select {
				case output <- reasoningDeltaTmp.Reasoning:
				case <-ctx.Done():
					return completionError(ctx.Err())
				}
			}
		}

		if toolCalls := delta.ToolCalls; len(toolCalls) > 0 {
			for _, toolCall := range toolCalls {
				invoke, ok := toolInvokes[toolCall.Index]
				if !ok {
					invoke = new(openai.ChatCompletionChunkChoiceDeltaToolCall)
					*invoke = toolCall
					toolInvokes[toolCall.Index] = invoke
				} else {
					if len(toolCall.Function.Name) > 0 {
						return completionError(errors.New("unexpected delta function name"))
					}
					invoke.Function.Arguments += toolCall.Function.Arguments
				}
			}
		}

		if content := delta.Content; len(content) > 0 {
			if !reasoningDone {
				reasoningDone = true
				if reasoningBuf.Len() > 0 && options.ShowReasoning {
					select {
					case output <- "\n\n**======== THINKING DONE ========**\n\n":
					case <-ctx.Done():
						return completionError(ctx.Err())
					}
				}
			}
			resultBuf.WriteString(content)
			select {
			case output <- content:
			case <-ctx.Done():
				return completionError(ctx.Err())
			}
		}
	}
	if err := stream.Err(); err != nil {
		var aerr *openai.Error
		if errors.As(err, &aerr) {
			return completionError(fmt.Errorf("%s %q: %d %s", aerr.Request.Method, aerr.Request.URL, aerr.Response.StatusCode, aerr.Message))
		}
		return completionError(err)
	}
	return completionState{
		reasoning: reasoningBuf.String(),
		result: resultBuf.String(),
	}
}
