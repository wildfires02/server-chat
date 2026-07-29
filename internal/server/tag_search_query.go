// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// tagSearchTokenType 描述旧版 Tag 搜索表达式中的词法类型。
type tagSearchTokenType int

const (
	tagSearchTokenNone tagSearchTokenType = iota
	tagSearchTokenQuote
	tagSearchTokenAnd
	tagSearchTokenOr
	tagSearchTokenEnd
	tagSearchTokenText
)

// tagSearchToken 是一个带 AND 或 OR 语义的已解析标签。
type tagSearchToken struct {
	operator tagSearchTokenType
	value    string
}

// tagSearchContext 保存解析一个 Tag 搜索表达式时的游标状态。
type tagSearchContext struct {
	// previousOperator 是当前标签左侧的操作符。
	previousOperator tagSearchTokenType
	// nextOperator 是当前标签右侧的操作符。
	nextOperator tagSearchTokenType
	// quoted 表示游标当前位于双引号字符串内。
	quoted bool
	// start 和 end 是当前标签在原始 UTF-8 字符串中的字节边界。
	start int
	end   int
}

// parseTagSearchQuery 解析旧版发现 Topic 使用的 Tag 搜索表达式。
//
// 空格表示必需标签，逗号两侧的标签表示可选标签，双引号允许标签中
// 包含空格。返回值依次为必需标签、可选标签和解析错误。
func parseTagSearchQuery(query string) ([]string, []string, error) {
	context := tagSearchContext{previousOperator: tagSearchTokenAnd}
	var tokens []tagSearchToken
	var previous tagSearchTokenType
	query = strings.TrimSpace(query)

	// index 是 UTF-8 字节下标，position 是用于错误信息的字符下标。
	for index, width, position := 0, 0, 0; previous != tagSearchTokenEnd; index, position = index+width, position+1 {
		emit := false
		current := tagSearchTokenText
		character, decodedWidth := utf8.DecodeRuneInString(query[index:])
		width = decodedWidth

		switch {
		case width == 0:
			current = tagSearchTokenEnd
		case character == '"':
			current = tagSearchTokenQuote
		case !context.quoted:
			switch character {
			case ' ', '\t':
				current = tagSearchTokenAnd
			case ',':
				current = tagSearchTokenOr
			}
		}

		if current == tagSearchTokenQuote {
			if context.quoted {
				context.quoted = false
			} else {
				if previous == tagSearchTokenText {
					return nil, nil, fmt.Errorf("第 %d 个字符附近缺少操作符", position)
				}
				context.quoted = true
			}
			current = tagSearchTokenText
		}

		switch current {
		case tagSearchTokenOr:
			if context.nextOperator == tagSearchTokenOr {
				return nil, nil, fmt.Errorf("第 %d 个字符附近存在无效操作符序列", position)
			}
			context.nextOperator = tagSearchTokenOr
			if previous == tagSearchTokenText {
				context.end = index
			}
		case tagSearchTokenAnd:
			if previous == tagSearchTokenText {
				context.end = index
				context.nextOperator = tagSearchTokenAnd
			} else if context.nextOperator != tagSearchTokenOr {
				context.nextOperator = tagSearchTokenAnd
			}
		case tagSearchTokenText:
			if previous == tagSearchTokenOr || previous == tagSearchTokenAnd {
				emit = true
			}
		case tagSearchTokenEnd:
			if previous == tagSearchTokenText {
				context.end = index
			}
			emit = true
		}

		if emit {
			if context.quoted && current == tagSearchTokenEnd {
				return nil, nil, fmt.Errorf("第 %d 个字符附近的引号未结束", position)
			}

			operator := context.previousOperator
			if context.nextOperator == tagSearchTokenOr {
				operator = tagSearchTokenOr
			}
			start, end := context.start, context.end
			if start < end && query[start] == '"' && query[end-1] == '"' {
				start++
				end--
			}
			if start < end {
				tokens = append(tokens, tagSearchToken{
					operator: operator,
					value:    strings.ToLower(query[start:end]),
				})
			}
			context.start = index
			context.previousOperator, context.nextOperator =
				context.nextOperator, tagSearchTokenNone
		}
		previous = current
	}

	var required []string
	var optional []string
	for _, token := range tokens {
		switch token.operator {
		case tagSearchTokenAnd:
			required = append(required, token.value)
		case tagSearchTokenOr:
			optional = append(optional, token.value)
		}
	}
	return required, optional, nil
}
