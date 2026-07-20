package tools

import (
	"context"
	"encoding/json"
	"sync"
)

type userMetaData struct {
	Region   string `json:"region"`
	Language string `json:"language"`
}

var (
	userMetaDataMux sync.RWMutex
	userMetaDatas   = make(map[string]*userMetaData)
)

func GetUserMetaData(userid string) userMetaData {
	userMetaDataMux.RLock()
	defer userMetaDataMux.RUnlock()

	data, ok := userMetaDatas[userid]
	if !ok {
		return userMetaData{}
	}
	return *data
}

func init() {
	tools["set_user_region"] = &ToolFunction{
		Description: "Update the user's region or location",
		ParametersSchema: map[string]any{
			"type": "object",
			"properties": map[string]map[string]any{
				"region": {
					"type":        "string",
					"description": "the user's region",
				},
			},
		},
		Callback: func(ctx context.Context, input string) (string, error) {
			var args struct {
				Region string `json:"region"`
			}
			if err := json.Unmarshal(([]byte)(input), &args); err != nil {
				return "", err
			}

			userid, ok := ctx.Value("userid").(string)
			if !ok {
				return "", nil
			}

			userMetaDataMux.Lock()
			defer userMetaDataMux.Unlock()
			data, ok := userMetaDatas[userid]
			if !ok {
				data = new(userMetaData)
				userMetaDatas[userid] = data
			}
			data.Region = args.Region
			return "success", nil
		},
	}
	tools["set_user_language"] = &ToolFunction{
		Description: "Update the user's preferred languages",
		ParametersSchema: map[string]any{
			"type":        "string",
			"description": "the user's preferred languages",
		},
		Callback: func(ctx context.Context, input string) (string, error) {
			var args struct {
				Language string `json:"language"`
			}
			if err := json.Unmarshal(([]byte)(input), &args); err != nil {
				return "", err
			}

			userid, ok := ctx.Value("userid").(string)
			if !ok {
				return "", nil
			}

			userMetaDataMux.Lock()
			defer userMetaDataMux.Unlock()
			data, ok := userMetaDatas[userid]
			if !ok {
				data = new(userMetaData)
				userMetaDatas[userid] = data
			}
			data.Language = args.Language
			return "success", nil
		},
	}
}
