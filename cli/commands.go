// Package main 实现服务端命令行客户端。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"chat/pbx"
)

// ParsedCmd 保存ParsedCmd的数据和运行状态。
type ParsedCmd struct {
	// ID 保存标识。
	ID string
	// Name 保存名称。
	Name string
	// IsLocal 指示是否启用或满足IsLocal。
	IsLocal bool
	// IsSynchronous 指示是否启用或满足IsSynchronous。
	IsSynchronous bool
	// VarName 保存Var名称。
	VarName string
	// FailOnError 保存FailOn错误。
	FailOnError bool
	// AsRoot 保存AsRoot。
	AsRoot bool
	// Msg 保存Msg。
	Msg *pbx.ClientMsg
	// MacroCmds 保存MacroCmds列表。
	MacroCmds []string
	// SleepMs 保存SleepMs。
	SleepMs int
	// LogValue 保存Log值。
	LogValue string
	// UseUser 指示是否启用或满足Use用户。
	UseUser string
	// UseTopic 指示是否启用或满足UseTopic。
	UseTopic string
	// IsExit 指示是否启用或满足IsExit。
	IsExit bool
}

// ParseCommandLine parses raw input line into a ParsedCmd or list of expanded macro commands.
func ParseCommandLine(line string, msgID int64, defaultUser, defaultTopic, apiKey string) (*ParsedCmd, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
		return nil, nil
	}

	tokens := SplitArgs(line)
	if len(tokens) == 0 {
		return nil, nil
	}

	idStr := strconv.FormatInt(msgID, 10)

	// Check if line starts with exit/quit/.exit/.quit
	firstToken := strings.ToLower(tokens[0])
	if firstToken == "exit" || firstToken == "quit" || firstToken == ".exit" || firstToken == ".quit" {
		return &ParsedCmd{IsExit: true}, nil
	}

	// Check if first token is a macro
	if IsMacro(firstToken) {
		expanded, err := ExpandMacro(tokens)
		if err != nil {
			return nil, err
		}
		return &ParsedCmd{MacroCmds: expanded}, nil
	}

	cmd := &ParsedCmd{
		ID:   idStr,
		Name: firstToken,
	}

	// Local control commands starting with '.'
	if strings.HasPrefix(firstToken, ".") {
		cmd.IsLocal = true
		switch firstToken {
		case ".await", ".must":
			cmd.IsSynchronous = true
			if firstToken == ".must" {
				cmd.FailOnError = true
			}
			idx := 1
			if len(tokens) > 1 && strings.HasPrefix(tokens[1], "$") {
				cmd.VarName = tokens[1]
				idx = 2
			}
			if idx >= len(tokens) {
				return nil, fmt.Errorf("%s requires a command to await", firstToken)
			}
			subLine := strings.Join(tokens[idx:], " ")
			return ParseCommandLine(subLine, msgID, defaultUser, defaultTopic, apiKey)

		case ".use":
			if len(tokens) > 1 {
				val := tokens[1]
				if strings.HasPrefix(val, "usr") || strings.HasPrefix(val, "user") {
					cmd.UseUser = val
				} else {
					cmd.UseTopic = val
				}
			}
			return cmd, nil

		case ".sleep":
			if len(tokens) > 1 {
				ms, _ := strconv.Atoi(tokens[1])
				cmd.SleepMs = ms
			}
			return cmd, nil

		case ".log":
			if len(tokens) > 1 {
				cmd.LogValue = strings.Join(tokens[1:], " ")
			}
			return cmd, nil

		case ".verbose":
			return cmd, nil

		case ".delmark":
			return cmd, nil
		}
	}

	// Standard gRPC commands
	switch firstToken {
	case "hi":
		msg := &pbx.ClientMsg{
			Message: &pbx.ClientMsg_Hi{
				Hi: &pbx.ClientHi{
					Id:         idStr,
					UserAgent:  "cli-go/3.1.0",
					Ver:        "0.29",
					Lang:       "EN",
					Background: true,
				},
			},
		}
		cmd.Msg = msg
		return cmd, nil

	case "login":
		return parseLoginCmd(cmd, tokens[1:], idStr)

	case "acc":
		return parseAccCmd(cmd, tokens[1:], idStr, defaultUser)

	case "sub":
		return parseSubCmd(cmd, tokens[1:], idStr, defaultTopic)

	case "leave":
		return parseLeaveCmd(cmd, tokens[1:], idStr, defaultTopic)

	case "pub":
		return parsePubCmd(cmd, tokens[1:], idStr, defaultTopic)

	case "get":
		return parseGetCmd(cmd, tokens[1:], idStr, defaultTopic)

	case "set":
		return parseSetCmd(cmd, tokens[1:], idStr, defaultTopic)

	case "del":
		return parseDelCmd(cmd, tokens[1:], idStr, defaultTopic)

	case "note":
		return parseNoteCmd(cmd, tokens[1:], idStr, defaultTopic)

	default:
		return nil, fmt.Errorf("unknown command or macro: %s", firstToken)
	}
}

// parseLoginCmd 将输入解析为登录Cmd。
func parseLoginCmd(cmd *ParsedCmd, args []string, idStr string) (*ParsedCmd, error) {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	scheme := fs.String("scheme", "basic", "Auth scheme (basic, token)")
	secret := fs.String("secret", "", "Secret or basic credentials user:pass")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	pos := fs.Args()
	sch := *scheme
	sec := *secret

	if len(pos) > 0 {
		if pos[0] == "basic" || pos[0] == "token" {
			sch = pos[0]
			if len(pos) > 1 {
				sec = pos[1]
			}
		} else if sec == "" {
			sec = pos[0]
		}
	}

	cmd.Msg = &pbx.ClientMsg{
		Message: &pbx.ClientMsg_Login{
			Login: &pbx.ClientLogin{
				Id:     idStr,
				Scheme: sch,
				Secret: []byte(sec),
			},
		},
	}
	return cmd, nil
}

// parseAccCmd 将输入解析为AccCmd。
func parseAccCmd(cmd *ParsedCmd, args []string, idStr, defaultUser string) (*ParsedCmd, error) {
	fs := flag.NewFlagSet("acc", flag.ContinueOnError)
	uname := fs.String("uname", "", "Username for basic login")
	password := fs.String("password", "", "Password for basic login")
	user := fs.String("user", "", "User ID to modify")
	email := fs.String("email", "", "Email address")
	tel := fs.String("tel", "", "Telephone number")
	fn := fs.String("fn", "", "Public name")
	photo := fs.String("photo", "", "Avatar image path or URL")
	asRoot := fs.Bool("as_root", false, "Execute as root")
	suspend := fs.Bool("suspend", false, "Suspend account status")
	auth := fs.String("auth", "", "Default auth access mode")
	anon := fs.String("anon", "", "Default anon access mode")
	credAdd := fs.String("cred_add", "", "Credential to add")
	credDel := fs.String("cred_del", "", "Credential to delete")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	cmd.AsRoot = *asRoot

	userId := *user
	if userId == "me" || userId == "" {
		userId = defaultUser
	}

	acc := &pbx.ClientAcc{
		Id:     idStr,
		UserId: userId,
	}

	if *uname != "" {
		acc.Scheme = "basic"
		acc.Secret = fmt.Appendf(nil, "%s:%s", *uname, *password)
		acc.Login = true
	}

	if *suspend {
		acc.State = "suspended"
	}

	if *fn != "" || *photo != "" || *auth != "" || *anon != "" {
		acc.Desc = &pbx.SetDesc{}
		if *fn != "" || *photo != "" {
			cardJSON, err := MakeTheCard(*fn, *photo, "")
			if err == nil {
				acc.Desc.Public = []byte(cardJSON)
			}
		}
		if *auth != "" || *anon != "" {
			acc.Desc.DefaultAcs = &pbx.DefaultAcsMode{
				Auth: *auth,
				Anon: *anon,
			}
		}
	}

	if *email != "" {
		acc.Cred = append(acc.Cred, &pbx.ClientCred{
			Method: "email",
			Value:  *email,
		})
	}
	if *tel != "" {
		acc.Cred = append(acc.Cred, &pbx.ClientCred{
			Method: "tel",
			Value:  *tel,
		})
	}
	if *credAdd != "" {
		m, v := ParseCred(*credAdd)
		acc.Cred = append(acc.Cred, &pbx.ClientCred{Method: m, Value: v})
	}
	if *credDel != "" {
		m, v := ParseCred(*credDel)
		acc.Cred = append(acc.Cred, &pbx.ClientCred{Method: m, Value: v, Response: "del"})
	}

	msg := &pbx.ClientMsg{
		Message: &pbx.ClientMsg_Acc{
			Acc: acc,
		},
	}

	if *asRoot {
		msg.Extra = &pbx.ClientExtra{
			OnBehalfOf: userId,
			AuthLevel:  pbx.AuthLevel_ROOT,
		}
	}

	cmd.Msg = msg
	return cmd, nil
}

// parseSubCmd 将输入解析为订阅Cmd。
func parseSubCmd(cmd *ParsedCmd, args []string, idStr, defaultTopic string) (*ParsedCmd, error) {
	topic := defaultTopic
	if len(args) > 0 {
		topic = args[0]
	}

	cmd.Msg = &pbx.ClientMsg{
		Message: &pbx.ClientMsg_Sub{
			Sub: &pbx.ClientSub{
				Id:    idStr,
				Topic: topic,
			},
		},
	}
	return cmd, nil
}

// parseLeaveCmd 将输入解析为LeaveCmd。
func parseLeaveCmd(cmd *ParsedCmd, args []string, idStr, defaultTopic string) (*ParsedCmd, error) {
	topic := defaultTopic
	if len(args) > 0 {
		topic = args[0]
	}

	cmd.Msg = &pbx.ClientMsg{
		Message: &pbx.ClientMsg_Leave{
			Leave: &pbx.ClientLeave{
				Id:    idStr,
				Topic: topic,
			},
		},
	}
	return cmd, nil
}

// parsePubCmd 将输入解析为PubCmd。
func parsePubCmd(cmd *ParsedCmd, args []string, idStr, defaultTopic string) (*ParsedCmd, error) {
	fs := flag.NewFlagSet("pub", flag.ContinueOnError)
	topicFlag := fs.String("topic", "", "Target topic")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	pos := fs.Args()
	topic := *topicFlag
	if topic == "" {
		if len(pos) > 0 && (strings.HasPrefix(pos[0], "usr") || strings.HasPrefix(pos[0], "grp") ||
			strings.HasPrefix(pos[0], "chn") || strings.HasPrefix(pos[0], "me") ||
			strings.HasPrefix(pos[0], "sys") || strings.HasPrefix(pos[0], "p2p")) {
			topic = pos[0]
			pos = pos[1:]
		} else {
			topic = defaultTopic
		}
	}

	contentStr := strings.Join(pos, " ")
	contentBytes, _ := json.Marshal(contentStr)

	cmd.Msg = &pbx.ClientMsg{
		Message: &pbx.ClientMsg_Pub{
			Pub: &pbx.ClientPub{
				Id:      idStr,
				Topic:   topic,
				Content: contentBytes,
			},
		},
	}
	return cmd, nil
}

// parseGetCmd 将输入解析为GetCmd。
func parseGetCmd(cmd *ParsedCmd, args []string, idStr, defaultTopic string) (*ParsedCmd, error) {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	desc := fs.Bool("desc", false, "Fetch topic description")
	sub := fs.Bool("sub", false, "Fetch topic subscriptions")
	data := fs.Bool("data", false, "Fetch topic messages")
	subTopic := fs.String("sub_topic", "", "Filter subscriptions by topic; use chn... to list channel readers")
	subUser := fs.String("sub_user", "", "Filter subscriptions by user ID")
	searchText := fs.String("search", "", "Search public peers or messages by keyword")
	searchScope := fs.String("scope", "", "Search scope: peers or topic")
	searchFrom := fs.String("from", "", "Only return messages sent by this user ID")
	searchKinds := fs.String("kinds", "", "Comma-separated message kinds")
	searchMinDate := fs.Int64("min_date", 0, "Minimum message creation time in Unix milliseconds")
	searchMaxDate := fs.Int64("max_date", 0, "Maximum message creation time in Unix milliseconds")
	searchCursor := fs.String("cursor", "", "Opaque cursor returned by the previous search page")
	limit := fs.Int("limit", 0, "Maximum number of results")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	pos := fs.Args()
	topic := defaultTopic
	if len(pos) > 0 {
		topic = pos[0]
	}

	query := &pbx.GetQuery{}
	if *desc {
		query.What += "desc "
		query.Desc = &pbx.GetOpts{}
	}
	if *sub || *subTopic != "" || *subUser != "" {
		query.What += "sub "
		query.Sub = &pbx.GetOpts{
			Topic: *subTopic,
			User:  *subUser,
			Limit: int32(*limit),
		}
	}
	if *data {
		query.What += "data "
		query.Data = &pbx.GetOpts{}
	}
	if *searchText != "" {
		query.What += "search "
		search := &pbx.SearchOpts{
			Query:      *searchText,
			Scope:      *searchScope,
			FromUserId: *searchFrom,
			MinDate:    *searchMinDate,
			MaxDate:    *searchMaxDate,
			Cursor:     *searchCursor,
			Limit:      int32(*limit),
		}
		for _, kind := range strings.Split(*searchKinds, ",") {
			if kind = strings.TrimSpace(kind); kind != "" {
				search.Kinds = append(search.Kinds, kind)
			}
		}
		query.Search = search
	}
	query.What = strings.TrimSpace(query.What)
	if query.What == "" {
		query.What = "desc sub"
		query.Desc = &pbx.GetOpts{}
		query.Sub = &pbx.GetOpts{}
	}

	cmd.Msg = &pbx.ClientMsg{
		Message: &pbx.ClientMsg_Get{
			Get: &pbx.ClientGet{
				Id:    idStr,
				Topic: topic,
				Query: query,
			},
		},
	}
	return cmd, nil
}

// parseSetCmd 将输入解析为SetCmd。
func parseSetCmd(cmd *ParsedCmd, args []string, idStr, defaultTopic string) (*ParsedCmd, error) {
	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	fn := fs.String("fn", "", "Public name")
	photo := fs.String("photo", "", "Avatar file or URL")
	private := fs.String("private", "", "Private comment")
	note := fs.String("note", "", "Account description")
	asRoot := fs.Bool("as_root", false, "Execute as root")
	user := fs.String("user", "", "Target topic member user ID")
	mode := fs.String("mode", "", "Raw ACL mode for the target member")
	role := fs.String("role", "", "Member role: admin/member/readonly/banned/publisher/subscriber")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *mode != "" && *role != "" {
		return nil, fmt.Errorf("set: mode and role are mutually exclusive")
	}

	pos := fs.Args()
	topic := defaultTopic
	if len(pos) > 0 {
		topic = pos[0]
	}

	cmd.AsRoot = *asRoot

	query := &pbx.SetQuery{}
	if *fn != "" || *photo != "" || *note != "" {
		cardJSON, err := MakeTheCard(*fn, *photo, *note)
		if err == nil {
			query.Desc = &pbx.SetDesc{
				Public: []byte(cardJSON),
			}
		}
	}

	if *private != "" {
		privBytes, _ := json.Marshal(map[string]string{"comment": *private})
		if query.Desc == nil {
			query.Desc = &pbx.SetDesc{}
		}
		query.Desc.Private = privBytes
	}
	if *user != "" || *mode != "" || *role != "" {
		query.Sub = &pbx.SetSub{
			UserId: *user,
			Mode:   *mode,
			Role:   *role,
		}
	}

	msg := &pbx.ClientMsg{
		Message: &pbx.ClientMsg_Set{
			Set: &pbx.ClientSet{
				Id:    idStr,
				Topic: topic,
				Query: query,
			},
		},
	}

	if *asRoot {
		msg.Extra = &pbx.ClientExtra{
			AuthLevel: pbx.AuthLevel_ROOT,
		}
	}

	cmd.Msg = msg
	return cmd, nil
}

// parseDelCmd 将输入解析为DelCmd。
func parseDelCmd(cmd *ParsedCmd, args []string, idStr, defaultTopic string) (*ParsedCmd, error) {
	fs := flag.NewFlagSet("del", flag.ContinueOnError)
	user := fs.String("user", "", "Delete user ID")
	member := fs.String("member", "", "Remove member subscription from the selected topic")
	asRoot := fs.Bool("as_root", false, "Delete as root")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *user != "" && *member != "" {
		return nil, fmt.Errorf("del: user and member are mutually exclusive")
	}

	pos := fs.Args()
	topic := defaultTopic
	if len(pos) > 0 {
		topic = pos[0]
	}

	what := pbx.ClientDel_MSG
	if *user != "" {
		what = pbx.ClientDel_USER
		topic = *user
	} else if *member != "" {
		what = pbx.ClientDel_SUB
	}

	msg := &pbx.ClientMsg{
		Message: &pbx.ClientMsg_Del{
			Del: &pbx.ClientDel{
				Id:     idStr,
				Topic:  topic,
				What:   what,
				UserId: *member,
			},
		},
	}

	if *asRoot {
		msg.Extra = &pbx.ClientExtra{
			AuthLevel: pbx.AuthLevel_ROOT,
		}
	}

	cmd.Msg = msg
	return cmd, nil
}

// parseNoteCmd 将输入解析为事件通知Cmd。
func parseNoteCmd(cmd *ParsedCmd, args []string, _, defaultTopic string) (*ParsedCmd, error) {
	topic := defaultTopic
	if len(args) > 0 {
		topic = args[0]
	}

	cmd.Msg = &pbx.ClientMsg{
		Message: &pbx.ClientMsg_Note{
			Note: &pbx.ClientNote{
				Topic: topic,
				What:  pbx.InfoNote_KP,
			},
		},
	}
	return cmd, nil
}
