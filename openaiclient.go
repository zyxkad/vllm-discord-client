package main

import (
	"context"
	"strings"

	"github.com/openai/openai-go/v3"
)

type Client struct {
	client openai.Client

	outputBuf strings.Builder
}

func (c *Client) StreamCompletion(ctx context.Context, messages []openai.ChatCompletionMessageParamUnion, output chan<- string) (string, error) {
	stream := c.client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{Messages: messages})
	defer stream.Close()
	defer c.outputBuf.Reset()

	for stream.Next() {
		chunk := stream.Current()
		t := chunk.Choices[0].Delta.Content
		c.outputBuf.WriteString(t)
		output <- t
	}
	totalOutput := c.outputBuf.String()
	if err := stream.Err(); err != nil {
		return totalOutput, err
	}
	return totalOutput, nil
}
