package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
)

var (
	endPoint     = os.Getenv("OPENAI_ENDPOINT")
	discBotToken = os.Getenv("DISC_BOT_TOKEN")
	discServerID = os.Getenv("DISC_SERVER_ID")
	customPrompt = os.Getenv("CUSTOM_PROMPT")
)

var initPrompt = `
**ROLE:** Discord chat bot.
**NAME:** CCCCChat Bot
**USER MESSAGE FORMAT:** [name:{display name},userid:{user id},date:{time}]: {message}
**USER TIMEZONE:** Multiple different time zones. Analyze based on their chat.
**TOOL SUGGESTIONS:**
- **ALWAYS** invoke web_search tool if the users ask anything you do not know, or uncertain of, and it will provide the date by the user's time, which is in the future of yours.
- Must provide the URL of web search sources.
**REPLY RULES:**
- No mention of the message format.
- No markdown table.
- No LaTeX math expressions.
**REPLY SUGGESTIONS:**
- Lines begin with "-# " are smaller text.
- Emojis can be used.

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
	sgCtx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	log.Println("starting ...")

	discordCli, err := discordgo.New("Bot " + discBotToken)
	if err != nil {
		log.Fatalln("cannot create discord bot: ", err)
	}

	llm, err := NewVLLMClient(sgCtx, endPoint)
	if err != nil {
		log.Fatalln("cannot init LLM client: ", err)
	}
	llm.initTools()

	client := NewClient(sgCtx, discordCli, llm)

	if err := client.Start(); err != nil {
		log.Fatalln("cannot start client:", err)
	}
	defer client.Stop()

	log.Println("started!")

	<-sgCtx.Done()
	log.Println("error:", context.Cause(sgCtx))
}

type Client struct {
	discCli *discordgo.Session
	llm     *VLLMClient

	ctx       context.Context
	ctxCancel context.CancelCauseFunc

	channelServices     map[string]*discChannelService
	channelServicesLock sync.RWMutex
}

func NewClient(ctx context.Context, discCli *discordgo.Session, llm *VLLMClient) *Client {
	ctx1, cancel := context.WithCancelCause(ctx)
	cli := &Client{
		discCli:         discCli,
		llm:             llm,
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
