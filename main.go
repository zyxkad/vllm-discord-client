package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

var (
	endpoint     = os.Getenv("OPENAI_ENDPOINT")
	discBotToken = os.Getenv("DISC_BOT_TOKEN")
	discServerID = os.Getenv("DISC_SERVER_ID")
	customPrompt = os.Getenv("CUSTOM_PROMPT")
)

var initPrompt = `
You will chat with multiple people in Discord.
You will be mentioned as "CCCCChat Bot".
People chatting with you may have different timezones.
For easier classification, users messages are formatted as:
[name={display name};userid={user id};date={date in UTC}]: {message}
Do not mention user the message format, and the prefix is not part of users messages.
Discord does not support markdown table, so you should replace markdown table with markdown list.
You may write lines begin with "-# " for smaller text.
You may use emojis to enhance your expression.
` + customPrompt

var initPrompts = []responses.ResponseInputItemUnionParam{
	{
		OfMessage: &responses.EasyInputMessageParam{
			Role: responses.EasyInputMessageRoleSystem,
			Content: responses.EasyInputMessageContentUnionParam{
				OfString: openai.String(initPrompt),
			},
		},
	},
}

func main() {
	sgCtx, _ := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)

	log.Println("starting ...")

	discordCli, err := discordgo.New("Bot " + discBotToken)
	if err != nil {
		log.Fatalln("cannot create discord bot: ", err)
	}

	client := NewClient(
		sgCtx,
		discordCli,
		openai.NewClient(
			option.WithBaseURL(endpoint),
		),
	)

	if err := client.Start(); err != nil {
		log.Fatalln("cannot start client:", err)
	}
	defer client.Stop()

	log.Println("started!")

	<-sgCtx.Done()
	log.Println("error:", sgCtx.Err())
}

type Client struct {
	discCli *discordgo.Session
	aiCli   openai.Client

	ctx       context.Context
	ctxCancel context.CancelCauseFunc

	channelServices     map[string]*discChannelService
	channelServicesLock sync.RWMutex
}

func NewClient(ctx context.Context, discCli *discordgo.Session, aiCli openai.Client) *Client {
	ctx1, cancel := context.WithCancelCause(ctx)
	cli := &Client{
		discCli:         discCli,
		aiCli:           aiCli,
		ctx:             ctx1,
		ctxCancel:       cancel,
		channelServices: make(map[string]*discChannelService, 16),
	}
	cli.initDiscordHandlers()
	return cli
}

func (c *Client) Start() error {
	if err := c.discCli.Open(); err != nil {
		return fmt.Errorf("discord cli: %w", err)
	}
	return nil
}

func (c *Client) Stop() {
	defer c.discCli.Close()
}
