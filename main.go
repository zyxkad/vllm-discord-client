package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

var (
	endpoint = os.Getenv("OPENAI_ENDPOINT")
	discBotToken = os.Getenv("DISC_BOT_TOKEN")
	discServerID = os.Getenv("DISC_SERVER_ID")
)

const discMsgMaxLength = 1999

func main() {
	sgCtx, _ := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	ctx, cancel := context.WithCancelCause(sgCtx)

	log.Println("starting ...")

	discordCli, err := discordgo.New("Bot " + discBotToken)
	if err != nil {
		log.Fatalln("cannot create discord bot: ", err)
	}

	aiCli := &Client{
		client: openai.NewClient(
			option.WithBaseURL(endpoint),
		),
	}

	messageInput := make(chan []openai.ChatCompletionMessageParamUnion, 0)
	streamOutput := make(chan string, 1024)
	responseEndFlag := make(chan struct{}, 1)

	inputUserMsg := func(username string, userid string, msg string) {
		messageInput <- []openai.ChatCompletionMessageParamUnion{{
			OfUser: &openai.ChatCompletionUserMessageParam{
				Name: openai.String(userid),
				Content: openai.ChatCompletionUserMessageParamContentUnion{
					OfString: openai.String(fmt.Sprintf("[name=%q;userid=%q]: %s", username, userid, msg)),
				},
			},
		}}
	}

	go func() {
		messageHistory := make([]openai.ChatCompletionMessageParamUnion, 0, 4)

		messageHistory = append(messageHistory, openai.ChatCompletionMessageParamUnion{
			OfSystem: &openai.ChatCompletionSystemMessageParam{
				Content: openai.ChatCompletionSystemMessageParamContentUnion{
					OfString: openai.String(
						"You will chat with multiple people at same time. " +
						"For easier classification, users messages are in the format: [name={display name};userid={user id}]: {message}\n" +
						"Do not mention user the message format, and the prefix is not part of users' messages.",
					),
				},
			},
		})

		for {
			select {
			case msgs := <-messageInput:
				messageHistory = append(messageHistory, msgs...)
			case <-ctx.Done():
				return
			}
			output, err := aiCli.StreamCompletion(ctx, messageHistory, streamOutput)
			if err != nil {
				cancel(err)
				return
			}
			streamOutput <- "\x00"
			messageHistory = append(messageHistory, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Content: openai.ChatCompletionAssistantMessageParamContentUnion{
						OfString: openai.String(output),
					},
				},
			})
		}
	} ()

	var replyingMsg *discordgo.Message

	go func() {
		timeout := 300 * time.Millisecond
		timeouter := time.NewTimer(timeout)

		resBuf := make([]string, 0, 16)

		refreshResBuf := func() {
			allRes := strings.Join(resBuf, "")
			resBuf = resBuf[:0]

			if replyingMsg == nil {
				return
			}
			var nextMsg string
			replyingMsg.Content = replyingMsg.Content + allRes
			if len(replyingMsg.Content) > discMsgMaxLength {
				replyingMsg.Content, nextMsg = splitMessage(replyingMsg.Content)
			}
			if _, err := discordCli.ChannelMessageEdit(replyingMsg.ChannelID, replyingMsg.ID, replyingMsg.Content); err != nil {
				log.Println("error: cannot edit replying message", replyingMsg.ID, ":", err)
			}
			if nextMsg != "" {
				var err error
				if replyingMsg, err = discordCli.ChannelMessageSend(replyingMsg.ChannelID, nextMsg); err != nil {
					log.Println("error: cannot send following reply message", replyingMsg.ID, ":", err)
				}
			}
		}

		for {
			select {
			case res := <-streamOutput:
				if res == "\x00" {
					refreshResBuf()
					replyingMsg = nil
					responseEndFlag <- struct{}{}
					break
				}

				resBuf = append(resBuf, res)
			case <-timeouter.C:
				timeouter.Reset(timeout)

				if len(resBuf) != 0 {
					refreshResBuf()
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	discordCli.Identify.Intents =
		discordgo.IntentGuilds |
		discordgo.IntentGuildMessages |
		discordgo.IntentGuildMessageTyping |
		discordgo.IntentMessageContent

	discMessageEvent := make(chan *discordgo.MessageCreate, 64)

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

	go func() {
		for {
			select {
			case event := <-discMessageEvent:
				replyMsg, err := discordCli.ChannelMessageSendReply(event.ChannelID, "*thinking...*", &discordgo.MessageReference{
					Type: discordgo.MessageReferenceTypeDefault,
					MessageID: event.ID,
					ChannelID: event.ChannelID,
					GuildID: event.GuildID,
				})
				if err != nil {
					log.Println("error when trying reply: ", err)
					return
				}
				replyMsg.Content = ""
				replyingMsg = replyMsg

				inputUserMsg(event.Author.DisplayName(), event.Author.Username, event.ContentWithMentionsReplaced())
			case <-ctx.Done():
				return
			}
			select {
			case <-responseEndFlag:
			case <-ctx.Done():
				return
			}
		}
	}()

	if err := discordCli.Open(); err != nil {
		log.Fatalln("cannot start discord bot:", err)
	}
	defer discordCli.Close()

	log.Println("started!")

	<-ctx.Done()
	log.Println("error:", ctx.Err())
}

var spliters = []string{
	"\n\n",
	"\n",
	". ",
	"? ",
	"! ",
	" ",
}

func splitMessage(message string) (l, r string) {
	for _, spliter := range spliters {
		i := strings.LastIndex(message[:discMsgMaxLength], spliter)
		if i >= 0 {
			return message[:i], message[i + 1:]
		}
	}
	return message[:discMsgMaxLength], message[discMsgMaxLength:]
}
