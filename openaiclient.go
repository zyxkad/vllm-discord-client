package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func (c *Client) StreamCompletion(ctx context.Context, messages []responses.ResponseInputItemUnionParam, output chan<- string) (string, error) {
	stream := c.aiCli.Responses.NewStreaming(ctx, responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: messages,
		},
		Reasoning: shared.ReasoningParam{
			Effort: shared.ReasoningEffortLow,
			Summary: shared.ReasoningSummaryDetailed,
		},
	})
	defer stream.Close()

	var outputBuf strings.Builder

loop:
	for stream.Next() {
		revent := stream.Current()
		switch event := revent.AsAny().(type) {
		case responses.ResponseErrorEvent:
			return outputBuf.String(), fmt.Errorf("remote: %s: %s", event.Code, event.Message)
		case responses.ResponseCreatedEvent:
		case responses.ResponseInProgressEvent:
		case responses.ResponseCompletedEvent:
			break loop
		case responses.ResponseReasoningTextDoneEvent:
			select {
			case output <- "\n\n------\n\n":
			case <-ctx.Done():
				return outputBuf.String(), ctx.Err()
			}
		case responses.ResponseReasoningTextDeltaEvent:
			t := event.Delta
			select {
			case output <- t:
			case <-ctx.Done():
				return outputBuf.String(), ctx.Err()
			}
		case responses.ResponseContentPartAddedEvent:
		case responses.ResponseContentPartDoneEvent:
		case responses.ResponseOutputItemAddedEvent:
		case responses.ResponseOutputItemDoneEvent:
		case responses.ResponseTextDoneEvent:
		case responses.ResponseTextDeltaEvent:
			t := event.Delta
			outputBuf.WriteString(t)
			select {
			case output <- t:
			case <-ctx.Done():
				return outputBuf.String(), ctx.Err()
			}
		default:
			if event != nil {
				log.Printf("unhandled stream event: %#v", event)
				continue
			}
			switch revent.Type {
			case "response.reasoning_part.added":
			case "response.reasoning_part.done":
			default:
				log.Println("unknown stream event:", revent.Type, revent.RawJSON())
				continue
			}
		}
	}
	totalOutput := outputBuf.String()
	if err := stream.Err(); err != nil {
		return totalOutput, err
	}
	return totalOutput, nil
}
