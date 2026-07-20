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

type spliterAndTail struct{
	spliter string
	tail string
}

var spliters = []spliterAndTail{
	{"\n\n", "-"},
	{"\n", ""},
	{". ", "-"},
	{"? ", "-"},
	{"! ", "-"},
	{" ", " -"},
}

func splitMessage(message string) (l, r string) {
	var i int
	for _, spliter := range spliters {
		i = strings.LastIndex(message[:discMsgMaxLength], spliter.spliter)
		if i >= 0 {
			return message[:i] + spliter.tail, message[i+1:]
		}
	}
	return message[:discMsgMaxLength] + "-", message[discMsgMaxLength:]
}

var codeBlockBeginRe = regexp.MustCompile("(?m)^(```+)[^`\\n]*$")

func fixSplitedCodeBlock(l, r string) (l2, r2 string) {
	i := 0
	for {
		idxs := codeBlockBeginRe.FindStringSubmatchIndex(l[i:])
		if len(idxs) == 0 {
			return l, r
		}
		codeEnd := l[i+idxs[2] : i+idxs[3]]
		codeEndRe := regexp.MustCompile("(?m)^" + codeEnd + "$")
		endI := codeEndRe.FindStringIndex(l[i+idxs[1]:])
		if len(endI) == 0 {
			codeStart := l[i+idxs[0]:i+idxs[1]] + "\n"
			return l + "\n" + codeEnd, codeStart + r
		}
		i += idxs[1] + endI[1]
	}
}
