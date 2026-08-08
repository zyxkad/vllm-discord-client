package tools

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

func init() {
	{
		browser := rod.New()
		go func() {
			if err := browser.Connect(); err != nil {
				log.Println("cannot open browser:", err)
				return
			}
			browser.Close()
		}()
	}

	tools["fetch_url"] = &ToolFunction{
		Description: "Fetch a website and then return its content in markdown format",
		ParametersSchema: map[string]any{
			"type": "object",
			"properties": map[string]map[string]any{
				"url": {
					"type":        "string",
					"description": "URL of the website need to fetch",
				},
			},
		},
		Callback: func(ctx context.Context, input string) (string, error) {
			var args struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(([]byte)(input), &args); err != nil {
				return "Tool invoke failed: parameter invalid: " + err.Error(), nil
			}

			browser := rod.New().Context(ctx)
			if err := browser.Connect(); err != nil {
				return "", err
			}
			defer browser.Close()

			page, err := browser.Page(proto.TargetCreateTarget{
				URL: args.URL,
			})
			if err != nil {
				return "Tool invoke failed: cannot open page: " + err.Error(), nil
			}

			page.WaitLoad()
			page.Timeout(time.Second * 5).WaitStable(700 * time.Millisecond)

			body, err := page.Element("body")
			if err != nil {
				return "Tool invoke failed: cannot find body tag: " + err.Error(), nil
			}

			bodyStr, err := body.HTML()
			if err != nil {
				return "", err
			}

			mdOut, err := htmltomarkdown.ConvertString(bodyStr)
			if err != nil {
				return "Tool invoke failed: cannot convert to markdown: " + err.Error(), nil
			}
			return mdOut, nil
		},
	}
}
