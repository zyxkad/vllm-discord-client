package tools

import (
	"context"
)

var tools = make(map[string]*ToolFunction)

type ToolFunction struct {
	Description      string
	ParametersSchema map[string]any
	Callback         func(ctx context.Context, input string) (string, error)
}

func Tools() map[string]*ToolFunction {
	return tools
}
