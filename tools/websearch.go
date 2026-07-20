package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	webSearchApi = os.Getenv("WEB_SEARCH_API")
)

func init() {
	webSearchClient := http.Client{
		Timeout: time.Minute,
	}

	tools["web_search"] = &ToolFunction{
		Description: "Provide accurate information on the user's date",
		ParametersSchema: map[string]any{
			"type": "object",
			"properties": map[string]map[string]any{
				"text": {
					"type":        "string",
					"description": "the text being used for web search",
				},
			},
		},
		Callback: func(ctx context.Context, input string) (string, error) {
			var args struct {
				Text string `json:"text"`
			}
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
}
