package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var (
	endpoint     = os.Getenv("OPENAI_ENDPOINT")
	discBotToken = os.Getenv("DISC_BOT_TOKEN")
	discServerID = os.Getenv("DISC_SERVER_ID")
	customPrompt = os.Getenv("CUSTOM_PROMPT")
)

var initPrompt = []openai.ChatCompletionMessageParamUnion{
	{
		OfSystem: &openai.ChatCompletionSystemMessageParam{
			Content: openai.ChatCompletionSystemMessageParamContentUnion{
				OfString: openai.String(
					"You will chat with multiple people at same time in Discord.\n" +
						"For easier classification, users messages are formatted as: " +
						"[name={display name};userid={user id};date={date in UTC}]: {message}\n" +
						"Do not mention user the message format, and the prefix is not part of users messages.\n" +
						"Discord does not support markdown table, so you should replace markdown table with markdown list. " +
						"You may write lines begin with \"-# \" for smaller text (or footnote). " +
						"You may use emojis to enhance your expression. \n" +
						"\n" + customPrompt,
				),
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

	discordCli.Identify.Intents = discordgo.IntentGuilds | discordgo.IntentGuildMessages | discordgo.IntentGuildMessageTyping | discordgo.IntentMessageContent

	client := NewClient(
		sgCtx,
		discordCli,
		openai.NewClient(
			option.WithBaseURL(endpoint),
		),
	)

	discMessageEvent := make(chan *discordgo.MessageCreate, 64)

	go func() {
		for {
			select {
			case event := <-discMessageEvent:
				client.OnUserMsg(event.Message)
			case <-sgCtx.Done():
				return
			}
		}
	}()

	discordCli.AddHandler(func(s *discordgo.Session, event *discordgo.MessageCreate) {
		if event.Author.Bot {
			return
		}
		if discServerID != "" && discServerID != event.GuildID {
			return
		}

		log.Printf("server=%s; name=%q; userid=%q: %s\n", event.GuildID, event.Author.DisplayName(), event.Author.Username, event.ContentWithMentionsReplaced())
		discMessageEvent <- event
	})

	if err := discordCli.Open(); err != nil {
		log.Fatalln("cannot start discord bot:", err)
	}
	defer discordCli.Close()

	log.Println("started!")

	<-sgCtx.Done()
	log.Println("error:", sgCtx.Err())
}
