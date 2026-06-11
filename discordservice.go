package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
)

const (
	discMsgMaxLength = 1950
	maxTextFileSize = 200 * 1024
)

var discNoMention = &discordgo.MessageAllowedMentions{}

func (c *Client) OnUserMsg(message *discordgo.Message) {
	channelID := message.ChannelID
	c.messageEventChMapLock.RLock()
	messageEventCh := c.messageEventChMap[channelID]
	c.messageEventChMapLock.RUnlock()
	if messageEventCh == nil {
		c.messageEventChMapLock.Lock()
		messageEventCh = c.messageEventChMap[channelID]
		if messageEventCh == nil {
			messageEventCh = make(chan *discordgo.Message, 0)
			go func() {
				defer func() {
					c.messageEventChMapLock.Lock()
					delete(c.messageEventChMap, channelID)
					c.messageEventChMapLock.Unlock()
				}()
				if err := c.runDiscChannelService(channelID, messageEventCh); err != nil {
					c.ctxCancel(err)
				}
			}()
			c.messageEventChMap[channelID] = messageEventCh
		}
		c.messageEventChMapLock.Unlock()
	}

	select {
	case messageEventCh <- message:
	case <-c.ctx.Done():
		return
	}
}

func (c *Client) resetMemory(channelID string, messageHistory []openai.ChatCompletionMessageParamUnion) []openai.ChatCompletionMessageParamUnion {
	messageHistory = append(messageHistory[:0], initPrompt...)

	if channel, err := c.discCli.State.Channel(channelID); err == nil {
		topic := channel.Topic
		if prompt, ok := strings.CutPrefix(topic, "AIP: "); ok {
			messageHistory = append(messageHistory, openai.ChatCompletionMessageParamUnion{
				OfSystem: &openai.ChatCompletionSystemMessageParam{
					Content: openai.ChatCompletionSystemMessageParamContentUnion{
						OfString: openai.String(prompt),
					},
				},
			})
		}
	}

	return messageHistory
}

func (c *Client) runDiscChannelService(channelID string, messageEventCh <-chan *discordgo.Message) error {
	messageHistory := make([]openai.ChatCompletionMessageParamUnion, 0, 4)
	messageHistory = c.resetMemory(channelID, messageHistory)

	for {
		select {
		case message := <-messageEventCh:
			if message.Content == "reset memory" {
				messageHistory = c.resetMemory(channelID, messageHistory)
				c.discSendReply(c.ctx, message, "***System**: Memory resetted!*")
				break
			}

			userid := message.Author.Username
			msgParts := make([]openai.ChatCompletionContentPartUnionParam, 0, 2)

			msgParts = append(msgParts, openai.ChatCompletionContentPartUnionParam{
				OfText: &openai.ChatCompletionContentPartTextParam{
					Text: fmt.Sprintf(
						"[name=%q;userid=%q;date=%s]: %s",
						message.Author.DisplayName(),
						userid,
						message.Timestamp.UTC().Format(time.DateTime),
						message.ContentWithMentionsReplaced(),
					),
				},
			})

			var buf bytes.Buffer
			for _, attachment := range message.Attachments {
				if strings.HasPrefix(attachment.ContentType, "image/") {
					msgParts = append(msgParts, openai.ChatCompletionContentPartUnionParam{
						OfImageURL: &openai.ChatCompletionContentPartImageParam{
							ImageURL: openai.ChatCompletionContentPartImageImageURLParam{
								URL: attachment.URL,
							},
						},
					})
					continue
				}
				if strings.HasPrefix(attachment.ContentType, "audio/") {
					continue
				}

				resp, err := http.DefaultClient.Get(attachment.URL)
				if err != nil {
					log.Println("cannot fetch discord attachment:", attachment.URL, ":", err)
					continue
				}
				n, err := io.Copy(&buf, io.LimitReader(resp.Body, maxTextFileSize + 1))
				resp.Body.Close()
				if err != nil || n > maxTextFileSize {
					continue
				}

				msgParts = append(msgParts, openai.ChatCompletionContentPartUnionParam{
					OfText: &openai.ChatCompletionContentPartTextParam{
						Text: fmt.Sprintf("Filename %q:\n````````\n%s\n````````", attachment.Filename, buf.String()),
					},
				})
			}

			messageHistory = append(messageHistory, openai.ChatCompletionMessageParamUnion{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Name:    openai.String(userid),
					Content: openai.ChatCompletionUserMessageParamContentUnion{OfArrayOfContentParts: msgParts},
				},
			})

			ctx, cancel := context.WithCancelCause(c.ctx)
			streamOutput := make(chan string, 64)

			go func() {
				output, err := c.StreamCompletion(ctx, messageHistory, streamOutput)
				if err != nil {
					if !errors.Is(err, ctx.Err()) {
						log.Println("error when streaming completion:", err)
						cancel(err)
					}
					return
				}
				messageHistory = append(messageHistory, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content: openai.ChatCompletionAssistantMessageParamContentUnion{
							OfString: openai.String(output),
						},
					},
				})
				close(streamOutput)
			}()

			cancel(c.discLiveReply(ctx, message, streamOutput))

			if err := c.ctx.Err(); err != nil {
				return err
			}
		case <-c.ctx.Done():
			return c.ctx.Err()
		}
	}
}

func (c *Client) discSendReply(ctx context.Context, replying *discordgo.Message, content string) (*discordgo.Message, error) {
	return c.discCli.ChannelMessageSendComplex(
		replying.ChannelID,
		&discordgo.MessageSend{
			Content:         content,
			AllowedMentions: discNoMention,
			Reference: &discordgo.MessageReference{
				Type:      discordgo.MessageReferenceTypeDefault,
				MessageID: replying.ID,
				ChannelID: replying.ChannelID,
				GuildID:   replying.GuildID,
			},
		},
		discordgo.WithContext(ctx),
	)
}

func (c *Client) discLiveReply(ctx context.Context, triggerMessage *discordgo.Message, streamOutput <-chan string) error {
	channelID := triggerMessage.ChannelID
	replyingMsg, err := c.discSendReply(ctx, triggerMessage, "*thinking...*")
	if err != nil {
		log.Println("error when creating reply message: ", err)
		return err
	}
	replyingMsg.Content = ""

	c.discCli.ChannelTyping(channelID, discordgo.WithContext(ctx))

	timeout := 400 * time.Millisecond
	timeouter := time.NewTimer(timeout)

	resBuf := make([]string, 0, 16)

	refreshResBuf := func() error {
		allRes := strings.Join(resBuf, "")
		resBuf = resBuf[:0]

		var nextMsg string
		replyingMsg.Content = replyingMsg.Content + allRes
		if len(replyingMsg.Content) > discMsgMaxLength {
			replyingMsg.Content, nextMsg = fixSplitedCodeBlock(splitMessage(replyingMsg.Content))
		}

		fixedMessage := fixMessage(replyingMsg.Content)
		if _, err := c.discCli.ChannelMessageEditComplex(
			&discordgo.MessageEdit{
				Channel:         replyingMsg.ChannelID,
				ID:              replyingMsg.ID,
				Content:         &fixedMessage,
				AllowedMentions: discNoMention,
			},
			discordgo.WithContext(ctx),
		); err != nil {
			log.Println("error: cannot edit replying message", replyingMsg.ID, ":", err)
			return err
		}

		if nextMsg != "" {
			var err error
			if replyingMsg, err = c.discCli.ChannelMessageSendComplex(
				replyingMsg.ChannelID,
				&discordgo.MessageSend{
					Content:         nextMsg,
					AllowedMentions: discNoMention,
				},
				discordgo.WithContext(ctx),
			); err != nil {
				log.Println("error: cannot send following reply message", replyingMsg.ID, ":", err)
				return err
			}
		}
		return nil
	}

	for {
		select {
		case res, ok := <-streamOutput:
			if !ok {
				return refreshResBuf()
			}

			resBuf = append(resBuf, res)
		case <-timeouter.C:
			timeouter.Reset(timeout)

			if len(resBuf) != 0 {
				c.discCli.ChannelTyping(channelID)
				if err := refreshResBuf(); err != nil {
					return err
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
