package main

import (
	"regexp"
	"strings"
)

var starSplitLineRe = regexp.MustCompile(`(?m)^\*\*+$`)

var messageFixers = []func(string) string{
	func(message string) string {
		return starSplitLineRe.ReplaceAllStringFunc(message, func(in string) string {
			return strings.Repeat("-", len(in))
		})
	},
}

func fixMessage(message string) string {
	for _, fix := range messageFixers {
		message = fix(message)
	}
	return message
}

type spliterAndTail struct {
	spliter string
	tail    string
}

var spliters = []spliterAndTail{
	{"\n\n", "\n-"},
	{"\n", ""},
	{". ", ".-"},
	{"? ", "?-"},
	{"! ", "!-"},
	{" ", " -"},
}

func splitMessage(message string) (l, r string) {
	var i int
	for _, spliter := range spliters {
		i = strings.LastIndex(message[:discMsgMaxLength], spliter.spliter)
		if i >= discMsgMinLength {
			return message[:i] + spliter.tail, message[i+len(spliter.spliter):]
		}
	}
	return message[:discMsgMaxLength] + "-", message[discMsgMaxLength:]
}

const codeBlockTag = "```"

func fixSplitedCodeBlock(l, r string) (l2, r2 string) {
	i := 0
	for {
		idx := strings.Index(l[i:], codeBlockTag)
		if idx < 0 {
			return l, r
		}
		endI := strings.Index(l[i+idx+len(codeBlockTag):], codeBlockTag)
		if endI < 0 {
			endI = strings.Index(r[:min(len(r), len(codeBlockTag)+1)], codeBlockTag)
			if endI == 0 {
				return l + "\n" + codeBlockTag, r[len(codeBlockTag):]
			}
			codeStart := codeBlockTag
			nlI := strings.IndexByte(l[i+idx+len(codeBlockTag):], '\n')
			if nlI >= 0 {
				codeStart = l[i+idx:i+idx+len(codeBlockTag)+nlI] + "\n"
			}
			return l + "\n" + codeBlockTag, codeStart + r
		}
		i += idx + len(codeBlockTag) + endI + len(codeBlockTag)
	}
}
