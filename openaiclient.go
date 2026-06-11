package main

import (
	"context"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
)

type Client struct {
	discCli *discordgo.Session
	aiCli   openai.Client

	ctx       context.Context
	ctxCancel context.CancelCauseFunc

	messageEventChMap     map[string]chan MessageEvent
	messageEventChMapLock sync.RWMutex
}

func NewClient(ctx context.Context, discCli *discordgo.Session, aiCli openai.Client) *Client {
	ctx1, cancel := context.WithCancelCause(ctx)
	return &Client{
		discCli:           discCli,
		aiCli:             aiCli,
		ctx:               ctx1,
		ctxCancel:         cancel,
		messageEventChMap: make(map[string]chan MessageEvent, 3),
	}
}

func (c *Client) StreamCompletion(ctx context.Context, messages []openai.ChatCompletionMessageParamUnion, output chan<- string) (string, error) {
	stream := c.aiCli.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{Messages: messages})
	defer stream.Close()

	var outputBuf strings.Builder

	for stream.Next() {
		chunk := stream.Current()
		t := chunk.Choices[0].Delta.Content
		outputBuf.WriteString(t)
		select {
		case output <- t:
		case <-ctx.Done():
			return outputBuf.String(), ctx.Err()
		}
	}
	totalOutput := outputBuf.String()
	if err := stream.Err(); err != nil {
		return totalOutput, err
	}
	return totalOutput, nil
}
