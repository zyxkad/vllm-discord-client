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
	"github.com/openai/openai-go/v3/shared"
)

var (
	endpoint     = os.Getenv("OPENAI_ENDPOINT")
	discBotToken = os.Getenv("DISC_BOT_TOKEN")
	discServerID = os.Getenv("DISC_SERVER_ID")
	customPrompt = os.Getenv("CUSTOM_PROMPT")
	webSearchApi = os.Getenv("WEB_SEARCH_API")
)

var initPrompt = `
You will chat with multiple people in Discord.
You may be mentioned as "CCCCChat Bot".
For easier classification, users messages are formatted as:
[name:{display name},userid:{user id},date:{UTC time}]: {message}
Do not mention user the message format, and the prefix is not part of users messages.
People chatting with you may have different timezones,
so you must convert UTC datetime to users local datetime when applicable.
You must not use markdown table.
You may write lines begin with "-# " for smaller text.
You may use emojis to enhance your expression.
Your knowledge base is outdated,
if the users ask anything you do not know, or uncertain of,
you must invoke web_search tool,
and you must provide the URL of web search sources.
` + customPrompt

var initPrompts = []openai.ChatCompletionMessageParamUnion{
	{
		OfSystem: &openai.ChatCompletionSystemMessageParam{
			Content: openai.ChatCompletionSystemMessageParamContentUnion{
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

	tools     map[string]ToolFunction
	toolParam []openai.ChatCompletionToolUnionParam
}

func NewClient(ctx context.Context, discCli *discordgo.Session, aiCli openai.Client) *Client {
	ctx1, cancel := context.WithCancelCause(ctx)
	cli := &Client{
		discCli:         discCli,
		aiCli:           aiCli,
		ctx:             ctx1,
		ctxCancel:       cancel,
		channelServices: make(map[string]*discChannelService, 16),
		tools:           make(map[string]ToolFunction),
	}
	cli.initDiscordHandlers()
	return cli
}

func (c *Client) Start() error {
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

	if err := c.discCli.Open(); err != nil {
		return fmt.Errorf("discord cli: %w", err)
	}
	return nil
}

func (c *Client) Stop() {
	defer c.discCli.Close()
}
