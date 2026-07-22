package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	discMsgMinLength = 800
	discMsgMaxLength = 1900
	maxTextFileSize  = 200 * 1024
)

var (
	discNoMention  = &discordgo.MessageAllowedMentions{}
	discManualStop = errors.New("manual stop triggered")
)

var discChatChannelPrefix = "ai-chat"

func (c *Client) initDiscordHandlers() {
	c.discCli.Identify.Intents = discordgo.IntentGuilds | discordgo.IntentGuildMessages | discordgo.IntentGuildMessageTyping | discordgo.IntentMessageContent

	allowedServers := make(map[string]struct{}, 0)
	for _, id := range strings.Split(discServerID, ",") {
		allowedServers[id] = struct{}{}
	}

	discMessageEvent := make(chan *discordgo.MessageCreate, 64)
	go func() {
		for {
			select {
			case event := <-discMessageEvent:
				c.serveUserMsg(event.Message)
			case <-c.ctx.Done():
				return
			}
		}
	}()
	c.discCli.AddHandler(func(s *discordgo.Session, event *discordgo.MessageCreate) {
		if event.Author.Bot {
			return
		}
		if len(allowedServers) > 0 {
			if _, ok := allowedServers[event.GuildID]; !ok {
				return
			}
		}
		if channel, err := c.discCli.State.Channel(event.ChannelID); err != nil || channel.IsThread() || !strings.HasPrefix(channel.Name, discChatChannelPrefix) {
			return
		}

		log.Printf(
			"server=%s; channel=%s; name=%q; user=%s: %s\n",
			event.GuildID,
			event.ChannelID,
			event.Author.DisplayName(),
			event.Author.Username,
			event.ContentWithMentionsReplaced(),
		)
		discMessageEvent <- event
	})

	c.discCli.AddHandler(func(s *discordgo.Session, event *discordgo.ChannelDelete) {
		c.DeleteChannelService(event.ID)
	})
}

func (c *Client) serveUserMsg(message *discordgo.Message) {
	service := c.GetOrCreateChannelService(message.ChannelID)
	select {
	case service.messageCh <- message:
	case <-service.ctx.Done():
		return
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
			Flags: discordgo.MessageFlagsSuppressEmbeds,
		},
		discordgo.WithContext(ctx),
	)
}

func (c *Client) discLiveReply(ctx context.Context, triggerMessage *discordgo.Message, streamOutput <-chan string) error {
	channelID := triggerMessage.ChannelID
	replyingMsg, err := c.discSendReply(ctx, triggerMessage, "*thinking...*")
	if err != nil {
		if ctx.Err() == nil {
			log.Println("error when creating reply message: ", err)
		}
		return err
	}
	currentContent := ""

	c.discCli.ChannelTyping(channelID, discordgo.WithContext(ctx))

	timeout := 500 * time.Millisecond
	timeouter := time.NewTimer(timeout)

	resBuf := make([]string, 0, 16)

	refreshResBuf := func() error {
		if len(resBuf) == 0 {
			return nil
		}
		allRes := strings.Join(resBuf, "")
		resBuf = resBuf[:0]

		if len(allRes) == 0 {
			return nil
		}

		var (
			nextMsg string
			err     error
		)

		currentContent = currentContent + allRes
		if len(currentContent) > discMsgMaxLength {
			currentContent, nextMsg = fixSplitedCodeBlock(splitMessage(currentContent))
		}

		fixedMessage := fixMessage(currentContent)
		if _, err = c.discCli.ChannelMessageEditComplex(
			&discordgo.MessageEdit{
				Channel:         channelID,
				ID:              replyingMsg.ID,
				Content:         &fixedMessage,
				AllowedMentions: discNoMention,
			},
			discordgo.WithContext(ctx),
		); err != nil {
			if ctx.Err() == nil {
				log.Println("error: cannot edit replying message", replyingMsg.ID, ":", err)
			}
			return err
		}

		for len(nextMsg) > 0 {
			currentContent, nextMsg = nextMsg, ""
			if len(currentContent) > discMsgMaxLength {
				currentContent, nextMsg = fixSplitedCodeBlock(splitMessage(currentContent))
				log.Printf("splited: left=%q righ=%q", currentContent, nextMsg)
			}
			if len(strings.Trim(currentContent, " \t\r\n")) == 0 {
				continue
			}
			fixedMessage := fixMessage(currentContent)
			if replyingMsg, err = c.discCli.ChannelMessageSendComplex(
				channelID,
				&discordgo.MessageSend{
					Content:         fixedMessage,
					AllowedMentions: discNoMention,
					Flags:           discordgo.MessageFlagsSuppressEmbeds,
				},
				discordgo.WithContext(ctx),
			); err != nil {
				if ctx.Err() == nil {
					log.Println("error: cannot send following reply message in", channelID, ":", err)
				}
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
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}

			resBuf = append(resBuf, res)
		case <-timeouter.C:
			timeouter.Reset(timeout)

			c.discCli.ChannelTyping(channelID)
			if len(resBuf) != 0 {
				if err := refreshResBuf(); err != nil {
					return err
				}
			}
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}
