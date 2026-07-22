package tools

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/dop251/goja"
)

func init() {
	tools["eval_javascript"] = &ToolFunction{
		Description: "Exceute JavaScript code in a safe sandbox.",
		ParametersSchema: map[string]any{
			"type": "object",
			"properties": map[string]map[string]any{
				"code": {
					"type":        "string",
					"description": "the code",
				},
			},
		},
		Callback: func(ctx context.Context, input string) (string, error) {
			var args struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(([]byte)(input), &args); err != nil {
				return "Tool invoke failed: parameter invalid: " + err.Error(), nil
			}

			vm := goja.New()

			tctx, tcancel := context.WithTimeout(ctx, 25*time.Second)
			context.AfterFunc(tctx, func() { vm.Interrupt(context.Cause(tctx)) })
			result, err := vm.RunString(args.Code)
			tcancel()

			var resultStr strings.Builder

			if err != nil {
				resultStr.WriteString("Status: error")
				resultStr.WriteByte('\n')
				resultStr.WriteString("Error Message: ")
				resultStr.WriteString(err.Error())
				return resultStr.String(), nil
			}

			resultStr.WriteString("Status: success")
			if goja.Undefined().Equals(result) {
				return resultStr.String(), nil
			}
			resultStr.WriteByte('\n')
			resultStr.WriteString("Returned Value: ")
			if obj, ok := result.(*goja.Object); ok {
				if bts, err := obj.MarshalJSON(); err != nil {
					resultStr.WriteString(obj.String())
				} else {
					resultStr.Write(bts)
				}
			} else {
				resultStr.WriteString(result.String())
			}
			return resultStr.String(), nil
		},
	}
}
