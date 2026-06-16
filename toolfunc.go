package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

type ToolFunction struct {
	Description      string
	ParametersSchema map[string]any
	Callback         func(ctx context.Context, input string) (string, error)
}

func (c *Client) initToolFunctions() {
	c.tools["web_search"] = webSearchTool
}

var webSearchClient = http.Client{}

type webSearchInput struct {
	Text string `json:"text"`
}

var webSearchTool = ToolFunction{
	Description: "Search information on live global internet",
	ParametersSchema: map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"properties": map[string]map[string]any{
			"text": {
				"type":        "string",
				"description": "the text being used for web search",
			},
		},
	},
	Callback: func(ctx context.Context, input string) (string, error) {
		log.Println("debug: web_search invoke:", input)

		var args webSearchInput
		if err := json.Unmarshal(([]byte)(input), &args); err != nil {
			return "", err
		}

		data := url.Values{}
		data.Add("q", args.Text)
		data.Add("format", "json")
		req, err := http.NewRequest(http.MethodPost, webSearchApi, strings.NewReader(data.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := webSearchClient.Do(req.WithContext(ctx))
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		var resultBuilder strings.Builder

		if _, err := io.Copy(&resultBuilder, resp.Body); err != nil {
			return "", err
		}

		return resultBuilder.String(), nil
	},
}
