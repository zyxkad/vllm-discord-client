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
**ROLE:** Discord chat bot.
**NAME:** CCCCChat Bot
**USER MESSAGE FORMAT:** [name:{display name},userid:{user id},date:{UTC time}]: {message}
**USER TIMEZONE:** Multiple different time zones. Analyze based on their chat.
**REPLY RULES:**
- No mention of the message format.
- No markdown table.
- No LaTeX math expressions.
**REPLY SUGGESTIONS:**
- Lines begin with "-# " are smaller text.
- Emojis can be used.
**TOOL SUGGESTIONS:**
- Invoke web_search tool if the users ask anything you do not know, or uncertain of.
- Must provide the URL of web search sources.

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
