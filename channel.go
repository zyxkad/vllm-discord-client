package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

type discChannelService struct {
	id string

	ctx    context.Context
	cancel context.CancelCauseFunc

	metaMux sync.RWMutex

	thinking bool
	prompt   string

	messageCh chan *discordgo.Message
}

func newDiscChannelService(ctx context.Context, id string) *discChannelService {
	cctx, cancel := context.WithCancelCause(ctx)
	return &discChannelService{
		id:        id,
		ctx:       cctx,
		cancel:    cancel,
		messageCh: make(chan *discordgo.Message, 0),
	}
}

func (c *Client) loadChannel(service *discChannelService) bool {
	service.metaMux.Lock()
	defer service.metaMux.Unlock()

	channel, err := c.discCli.State.Channel(service.id)
	if err != nil {
		return false
	}

	s := bufio.NewScanner(strings.NewReader(channel.Topic))
	for s.Scan() {
		t := s.Text()
		if think, ok := strings.CutPrefix(t, "think: "); ok {
			service.thinking, _ = strconv.ParseBool(think)
		} else if prompt, ok := strings.CutPrefix(t, "prompt: "); ok {
			service.prompt = prompt
		}
	}

	return true
}

func (s *discChannelService) resetMemory(messageHistory []responses.ResponseInputItemUnionParam) []responses.ResponseInputItemUnionParam {
	messageHistory = append(messageHistory[:0], initPrompts...)
	if len(s.prompt) > 0 {
		messageHistory = append(messageHistory, responses.ResponseInputItemUnionParam{
			OfMessage: &responses.EasyInputMessageParam{
				Role: responses.EasyInputMessageRoleSystem,
				Content: responses.EasyInputMessageContentUnionParam{
					OfString: openai.String(s.prompt),
				},
			},
		})
	}
	return messageHistory
}

func (c *Client) GetOrCreateChannelService(channelID string) *discChannelService {
	c.channelServicesLock.RLock()
	service := c.channelServices[channelID]
	c.channelServicesLock.RUnlock()
	if service != nil {
		return service
	}

	c.channelServicesLock.Lock()
	defer c.channelServicesLock.Unlock()
	service = c.channelServices[channelID]
	if service != nil {
		return service
	}

	service = newDiscChannelService(c.ctx, channelID)
	c.loadChannel(service)

	go func() {
		defer service.cancel(nil)
		defer func() {
			if service.ctx.Err() == discManualStop {
				return
			}
			c.channelServicesLock.Lock()
			defer c.channelServicesLock.Unlock()
			if _, ok := c.channelServices[channelID]; ok {
				delete(c.channelServices, channelID)
			}
		}()
		c.runDiscChannelService(service)
	}()
	c.channelServices[channelID] = service
	return service
}

func (c *Client) DeleteChannelService(channelID string) bool {
	c.channelServicesLock.RLock()
	service := c.channelServices[channelID]
	c.channelServicesLock.RUnlock()
	if service == nil {
		return false
	}

	c.channelServicesLock.Lock()
	defer c.channelServicesLock.Unlock()
	service = c.channelServices[channelID]
	if service == nil {
		return false
	}
	delete(c.channelServices, channelID)
	service.cancel(discManualStop)
	return true
}

func (c *Client) runDiscChannelService(service *discChannelService) {
	ctx := service.ctx
	messageCh := service.messageCh

	messageHistory := make([]responses.ResponseInputItemUnionParam, 0, 4)
	messageHistory = service.resetMemory(messageHistory)

	for {
		select {
		case message := <-messageCh:
			if message.Content == "reset" {
				c.loadChannel(service)
				messageHistory = service.resetMemory(messageHistory)
				c.discSendReply(ctx, message, "**System**: Memory resetted!")
				break
			}

			userid := message.Author.Username
			msgParts := make(responses.ResponseInputMessageContentListParam, 0, 2)

			msgParts = append(msgParts, responses.ResponseInputContentUnionParam{
				OfInputText: &responses.ResponseInputTextParam{
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
					msgParts = append(msgParts, responses.ResponseInputContentUnionParam{
						OfInputImage: &responses.ResponseInputImageParam{
							Detail: responses.ResponseInputImageDetailAuto,
							ImageURL: openai.String(attachment.URL),
						},
					})
					continue
				}
				if strings.HasPrefix(attachment.ContentType, "audio/") {
					// msgParts = append(msgParts, responses.ResponseInputContentUnionParam{
					// 	OfInputFile: &responses.ResponseInputFileParam{
					// 		FileURL: openai.String(attachment.URL),
					// 	},
					// })
					continue
				}

				resp, err := http.DefaultClient.Get(attachment.URL)
				if err != nil {
					log.Println("cannot fetch discord attachment:", attachment.URL, ":", err)
					continue
				}
				n, err := io.Copy(&buf, io.LimitReader(resp.Body, maxTextFileSize+1))
				resp.Body.Close()
				if err != nil || n > maxTextFileSize {
					continue
				}

				msgParts = append(msgParts, responses.ResponseInputContentUnionParam{
					OfInputText: &responses.ResponseInputTextParam{
						Text: fmt.Sprintf("Filename %q:\n````````\n%s\n````````", attachment.Filename, buf.String()),
					},
				})
			}

			messageHistory = append(messageHistory, responses.ResponseInputItemUnionParam{
				OfMessage: &responses.EasyInputMessageParam{
					Role: responses.EasyInputMessageRoleUser,
					Content: responses.EasyInputMessageContentUnionParam{
						OfInputItemContentList: msgParts,
					},
				},
			})

			cctx, cancel := context.WithCancelCause(ctx)
			streamOutput := make(chan string, 64)

			go func() {
				output, err := c.StreamCompletion(cctx, messageHistory, streamOutput)
				if err != nil {
					if !errors.Is(err, cctx.Err()) {
						log.Println("error when streaming completion:", err)
						cancel(err)
					}
					return
				}
				messageHistory = append(messageHistory, responses.ResponseInputItemUnionParam{
					OfMessage: &responses.EasyInputMessageParam{
						Role: responses.EasyInputMessageRoleAssistant,
						Content: responses.EasyInputMessageContentUnionParam{
							OfString: openai.String(output),
						},
					},
				})
				close(streamOutput)
			}()

			cancel(c.discLiveReply(cctx, message, streamOutput))

			if err := ctx.Err(); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
