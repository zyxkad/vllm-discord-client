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
)

type discChannelService struct {
	id string

	ctx    context.Context
	cancel context.CancelCauseFunc

	metaMux sync.RWMutex

	options completionOptions
	prompt  string

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

	service.options = completionOptions{}
	service.prompt = ""

	s := bufio.NewScanner(strings.NewReader(channel.Topic))
	for s.Scan() {
		t := s.Text()
		if think, ok := strings.CutPrefix(t, "think="); ok {
			service.options.Reasoning, _ = strconv.Atoi(think)
		} else if showThink, ok := strings.CutPrefix(t, "show-think="); ok {
			service.options.ShowReasoning, _ = strconv.ParseBool(showThink)
		} else if prompt, ok := strings.CutPrefix(t, "prompt="); ok {
			service.prompt = prompt
		}
	}

	return true
}

func (s *discChannelService) getOptions() completionOptions {
	s.metaMux.RLock()
	defer s.metaMux.RUnlock()
	return s.options
}

func (s *discChannelService) getPrompt() string {
	s.metaMux.RLock()
	defer s.metaMux.RUnlock()
	return s.prompt
}

func (s *discChannelService) resetMemory(messageHistory []openai.ChatCompletionMessageParamUnion) []openai.ChatCompletionMessageParamUnion {
	messageHistory = append(messageHistory[:0], initPrompts...)
	prompt := s.getPrompt()
	if len(prompt) > 0 {
		messageHistory = append(messageHistory, openai.ChatCompletionMessageParamUnion{
			OfSystem: &openai.ChatCompletionSystemMessageParam{
				Content: openai.ChatCompletionSystemMessageParamContentUnion{
					OfString: openai.String(prompt),
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
			if context.Cause(service.ctx) == discManualStop {
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

	messageHistory := make([]openai.ChatCompletionMessageParamUnion, 0, 4)
	messageHistory = service.resetMemory(messageHistory)

	for {
		select {
		case message := <-messageCh:
			if message.Content == "reset" {
				c.DeleteChannelService(service.id)
				tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				_, err := c.discSendReply(tctx, message, "**System**: Memory resetted!")
				cancel()
				if err != nil {
					log.Println("cannot send discord reply:", err)
				}
				return
			}

			userid := message.Author.Username
			msgParts := make([]openai.ChatCompletionContentPartUnionParam, 0, 2)

			msgParts = append(msgParts, openai.ChatCompletionContentPartUnionParam{
				OfText: &openai.ChatCompletionContentPartTextParam{
					Text: fmt.Sprintf(
						"[name:%q,userid:%q,date:%q]: %s",
						message.Author.DisplayName(),
						userid,
						message.Timestamp.UTC().Format(time.DateTime)+" UTC",
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
								URL:    attachment.URL,
								Detail: "auto",
							},
						},
					})
					continue
				}
				if strings.HasPrefix(attachment.ContentType, "audio/") {
					// msgParts = append(msgParts, openai.ChatCompletionContentPartUnionParam{
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

				msgParts = append(msgParts, openai.ChatCompletionContentPartUnionParam{
					OfText: &openai.ChatCompletionContentPartTextParam{
						Text: fmt.Sprintf("Filename %q:\n````````\n%s\n````````", attachment.Filename, buf.String()),
					},
				})
			}

			messageHistory = append(messageHistory, openai.ChatCompletionMessageParamUnion{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Content: openai.ChatCompletionUserMessageParamContentUnion{
						OfArrayOfContentParts: msgParts,
					},
				},
			})

			cctx, cancel := context.WithCancelCause(ctx)
			streamOutput := make(chan string, 64)

			cctx = context.WithValue(cctx, "userid", userid)

			go func() {
				newMessageHistory, err := c.llm.StreamCompletion(cctx, messageHistory, service.getOptions(), streamOutput)
				if err != nil {
					if !errors.Is(err, context.Cause(cctx)) {
						log.Println("error when streaming completion:", err)
						cancel(err)
					}
					return
				}
				messageHistory = newMessageHistory
				close(streamOutput)
			}()

			err := c.discLiveReply(cctx, message, streamOutput)
			if err != nil {
				tmpCtx, tmpCancel := context.WithTimeout(context.WithoutCancel(cctx), 3*time.Second)
				go func() {
					defer tmpCancel()
					c.discSendReply(tmpCtx, message, "**System:** internal error")
				}()
			}
			cancel(err)

			if ctx.Err() != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
