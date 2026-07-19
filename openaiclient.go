package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

type VLLMClient struct {
	endPoint *url.URL
	httpCli  *http.Client
	aiCli    openai.Client

	sleepMux   sync.RWMutex
	workSignal chan bool

	tools     map[string]ToolFunction
	toolParam []openai.ChatCompletionToolUnionParam
}

func NewVLLMClient(ctx context.Context, endPoint string) (*VLLMClient, error) {
	httpCli := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:          10,
			IdleConnTimeout:       10 * time.Minute,
			ResponseHeaderTimeout: 3 * time.Minute,
		},
	}
	endPointUrl, err := url.Parse(endPoint)
	if err != nil {
		return nil, err
	}
	c := &VLLMClient{
		endPoint: endPointUrl,
		httpCli:  httpCli,
		aiCli: openai.NewClient(
			option.WithHTTPClient(httpCli),
			option.WithBaseURL(endPointUrl.JoinPath("/v1").String()),
		),
		workSignal: make(chan bool, 8),
		tools:      make(map[string]ToolFunction),
	}
	go func() {
		var workCount atomic.Int32

		idleTimeout := 10 * time.Minute
		sleepTimer := time.NewTimer(idleTimeout)
		defer func() {
			sleepTimer.Stop()
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case isDone := <-c.workSignal:
				if isDone {
					if workCount.Add(-1) <= 0 {
						sleepTimer = time.NewTimer(idleTimeout)
					}
				} else if workCount.Add(1) == 1 {
					sleepTimer.Stop()
				}
			case <-sleepTimer.C:
				if workCount.Load() > 0 {
					return
				}
				sleepTimer = time.NewTimer(idleTimeout)
				go func() {
					c.TrySleep(ctx)
					if workCount.Load() > 0 {
						c.TryWakeup(ctx)
					}
				}()
			}
		}
	}()
	return c, nil
}

type completionOptions struct {
	Reasoning     int
	ShowReasoning bool
}

type completionState struct {
	err         error
	reasoning   string
	toolInvokes map[int64]*openai.ChatCompletionChunkChoiceDeltaToolCall
	result      string
}

func completionError(err error) completionState {
	return completionState{err: err}
}

func (c *VLLMClient) initTools() {
	c.initToolFunctions()
	c.toolParam = make([]openai.ChatCompletionToolUnionParam, 0, len(c.tools))
	for name, fn := range c.tools {
		c.toolParam = append(c.toolParam, openai.ChatCompletionToolUnionParam{
			OfFunction: &openai.ChatCompletionFunctionToolParam{
				Function: shared.FunctionDefinitionParam{
					Name:        name,
					Description: openai.String(fn.Description),
					Parameters:  fn.ParametersSchema,
				},
			},
		})
	}
}

func (c *VLLMClient) DoReq(ctx context.Context, method string, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	return c.httpCli.Do(req)
}

func (c *VLLMClient) DoJsonReq(ctx context.Context, method string, u *url.URL, data any) (*http.Response, error) {
	buf, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.httpCli.Do(req)
}

func (c *VLLMClient) IsSleeping(ctx context.Context) bool {
	c.sleepMux.RLock()
	defer c.sleepMux.RUnlock()

	u := c.endPoint.JoinPath("/is_sleeping")
	resp, err := c.DoReq(ctx, http.MethodGet, u)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var payload struct {
		IsSleeping bool `json:"is_sleeping"`
	}
	if json.NewDecoder(resp.Body).Decode(&payload) != nil {
		return false
	}
	return payload.IsSleeping
}

func (c *VLLMClient) TrySleep(ctx context.Context) error {
	log.Println("trying to sleep...")

	c.sleepMux.Lock()
	defer c.sleepMux.Unlock()

	u := c.endPoint.JoinPath("/sleep")
	q := url.Values{}
	q.Set("level", "1")
	// TODO: level=2 sleep will corrupt completion
	// q.Set("level", "2")
	u.RawQuery = q.Encode()
	resp, err := c.DoReq(ctx, http.MethodPost, u)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status from /sleep: %d %s", resp.StatusCode, resp.Status)
	}
	return nil
}

func (c *VLLMClient) TryWakeup(ctx context.Context) error {
	log.Println("trying to wake up...")

	c.sleepMux.Lock()
	defer c.sleepMux.Unlock()

	wakeUpUrl := c.endPoint.JoinPath("/wake_up")
	// q := url.Values{}
	// q.Set("tags", "weights")
	// wakeUpUrl.RawQuery = q.Encode()
	resp, err := c.DoReq(ctx, http.MethodPost, wakeUpUrl)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status from %s: %d %s", resp.Request.URL.String(), resp.StatusCode, resp.Status)
	}

	// resp, err = c.DoJsonReq(ctx, http.MethodPost, c.endPoint.JoinPath("/collective_rpc"), map[string]string{"method": "reload_weights"})
	// if err != nil {
	// 	return err
	// }
	// resp.Body.Close()
	// if resp.StatusCode != http.StatusOK {
	// 	return fmt.Errorf("unexpected status from %s: %d %s", resp.Request.URL.String(), resp.StatusCode, resp.Status)
	// }

	// clear(q)
	// q.Set("tags", "kv_cache")
	// wakeUpUrl.RawQuery = q.Encode()
	// resp, err = c.DoReq(ctx, http.MethodPost, wakeUpUrl)
	// if err != nil {
	// 	return err
	// }
	// resp.Body.Close()
	// if resp.StatusCode != http.StatusOK {
	// 	return fmt.Errorf("unexpected status from %s: %d %s", resp.Request.URL.String(), resp.StatusCode, resp.Status)
	// }

	return nil
}

func (c *VLLMClient) StreamCompletion(
	ctx context.Context,
	messages []openai.ChatCompletionMessageParamUnion,
	options completionOptions,
	output chan<- string,
) ([]openai.ChatCompletionMessageParamUnion, error) {
	c.workSignal <- false
	defer func() {
		c.workSignal <- false
	}()
	if c.IsSleeping(ctx) {
		if err := c.TryWakeup(ctx); err != nil {
			return nil, err
		}
	}

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
							Name:      invoke.Function.Name,
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
				res, err := tool.Callback(ctx, invoke.Function.Arguments)
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

func (c *VLLMClient) streamCompletionPart(
	ctx context.Context,
	messages []openai.ChatCompletionMessageParamUnion,
	options completionOptions,
	output chan<- string,
) completionState {
	params := openai.ChatCompletionNewParams{
		Messages:         messages,
		FrequencyPenalty: openai.Float(0.2),
		Tools:            c.toolParam,
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
		resultBuf     strings.Builder
		reasoningBuf  strings.Builder
		reasoningDone = false
		toolInvokes   = make(map[int64]*openai.ChatCompletionChunkChoiceDeltaToolCall)
	)

	for stream.Next() {
		event := stream.Current()
		choice := event.Choices[0]
		switch choice.FinishReason {
		case "stop":
			break
		case "tool_calls":
			if options.ShowReasoning {
				select {
				case output <- "\n\n**======== INVOKING TOOLS ========**\n\n":
				case <-ctx.Done():
					return completionError(context.Cause(ctx))
				}
			}
			return completionState{
				reasoning:   reasoningBuf.String(),
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
		if err := json.Unmarshal(([]byte)(delta.RawJSON()), &reasoningDeltaTmp); err == nil && len(reasoningDeltaTmp.Reasoning) > 0 {
			if reasoningDone {
				return completionError(errors.New("unexpected reasoning chunk after reasoning is done"))
			}
			reasoningBuf.WriteString(reasoningDeltaTmp.Reasoning)
			if options.ShowReasoning {
				select {
				case output <- reasoningDeltaTmp.Reasoning:
				case <-ctx.Done():
					return completionError(context.Cause(ctx))
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
						return completionError(context.Cause(ctx))
					}
				}
			}
			resultBuf.WriteString(content)
			select {
			case output <- content:
			case <-ctx.Done():
				return completionError(context.Cause(ctx))
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
		result:    resultBuf.String(),
	}
}
