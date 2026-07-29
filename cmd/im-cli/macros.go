// Package main 实现 IM 命令行客户端。
package main

import (
	"flag"
	"fmt"
	"strings"
)

// IsMacro checks if the first token is a known macro name.
func IsMacro(name string) bool {
	switch name {
	case "usermod", "useradd", "userdel", "passwd", "chacs", "chcred", "resolve", "thecard":
		return true
	default:
		return false
	}
}

// ExpandMacro expands macro commands into basic tn-cli commands.
func ExpandMacro(tokens []string) ([]string, error) {
	if len(tokens) == 0 {
		return nil, nil
	}

	macroName := tokens[0]
	args := tokens[1:]

	switch macroName {
	case "usermod":
		return expandUsermod(args)
	case "useradd":
		return expandUseradd(args)
	case "userdel":
		return expandUserdel(args)
	case "passwd":
		return expandPasswd(args)
	case "chacs":
		return expandChacs(args)
	case "chcred":
		return expandChcred(args)
	case "resolve":
		return expandResolve(args)
	case "thecard":
		return expandThecard(args)
	default:
		return nil, fmt.Errorf("unknown macro: %s", macroName)
	}
}

// expandUsermod 完成expandUsermod所需的内部处理。
func expandUsermod(args []string) ([]string, error) {
	fs := flag.NewFlagSet("usermod", flag.ContinueOnError)
	suspend := fs.Bool("suspend", false, "Suspend account")
	suspendShort := fs.Bool("L", false, "Suspend account")
	unsuspend := fs.Bool("unsuspend", false, "Unsuspend account")
	unsuspendShort := fs.Bool("U", false, "Unsuspend account")
	name := fs.String("name", "", "Public name")
	avatar := fs.String("avatar", "", "Avatar file or URL")
	comment := fs.String("comment", "", "Private comment")
	note := fs.String("note", "", "Account description")
	trusted := fs.String("trusted", "", "Trusted marker (verified, staff)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	posArgs := fs.Args()
	if len(posArgs) < 1 {
		return nil, fmt.Errorf("usermod requires userid argument")
	}
	userid := posArgs[0]

	isSuspend := *suspend || *suspendShort
	isUnsuspend := *unsuspend || *unsuspendShort

	if isSuspend && isUnsuspend {
		return nil, fmt.Errorf("cannot specify both suspend and unsuspend")
	}

	var cmds []string
	if isSuspend {
		cmds = append(cmds, fmt.Sprintf("acc --user=%s --as_root --suspend=true", userid))
	} else if isUnsuspend {
		cmds = append(cmds, fmt.Sprintf("acc --user=%s --as_root --suspend=false", userid))
	}

	if *name != "" || *avatar != "" || *comment != "" || *note != "" || *trusted != "" {
		cmd := fmt.Sprintf(".must $temp set %s", userid)
		if *name != "" {
			cmd += fmt.Sprintf(" --fn=%q", *name)
		}
		if *avatar != "" {
			cmd += fmt.Sprintf(" --photo=%q", *avatar)
		}
		if *comment != "" {
			cmd += fmt.Sprintf(" --private=%q", *comment)
		}
		if *note != "" {
			cmd += fmt.Sprintf(" --note=%q", *note)
		}
		if *trusted != "" {
			cmd += fmt.Sprintf(" --trusted=%q --as_root", *trusted)
		}
		cmds = append(cmds, cmd)
	}

	return cmds, nil
}

// expandUseradd 完成expandUseradd所需的内部处理。
func expandUseradd(args []string) ([]string, error) {
	fs := flag.NewFlagSet("useradd", flag.ContinueOnError)
	email := fs.String("email", "", "Email address")
	tel := fs.String("tel", "", "Telephone number")
	fn := fs.String("fn", "", "Public name")
	avatar := fs.String("avatar", "", "Avatar file or URL")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	posArgs := fs.Args()
	if len(posArgs) < 2 {
		return nil, fmt.Errorf("useradd requires username and password arguments")
	}
	username := posArgs[0]
	password := posArgs[1]

	cmd := fmt.Sprintf("acc --uname=%s --password=%s", username, password)
	if *email != "" {
		cmd += fmt.Sprintf(" --email=%q", *email)
	}
	if *tel != "" {
		cmd += fmt.Sprintf(" --tel=%q", *tel)
	}
	if *fn != "" {
		cmd += fmt.Sprintf(" --fn=%q", *fn)
	}
	if *avatar != "" {
		cmd += fmt.Sprintf(" --photo=%q", *avatar)
	}

	return []string{cmd}, nil
}

// expandUserdel 完成expandUserdel所需的内部处理。
func expandUserdel(args []string) ([]string, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("userdel requires userid argument")
	}
	userid := args[0]
	return []string{fmt.Sprintf("del --user=%s --as_root", userid)}, nil
}

// expandPasswd 完成expandPasswd所需的内部处理。
func expandPasswd(args []string) ([]string, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("passwd requires userid and new_password arguments")
	}
	userid := args[0]
	newPass := args[1]
	return []string{fmt.Sprintf("acc --user=%s --password=%s --as_root", userid, newPass)}, nil
}

// expandChacs 完成expandChacs所需的内部处理。
func expandChacs(args []string) ([]string, error) {
	fs := flag.NewFlagSet("chacs", flag.ContinueOnError)
	auth := fs.String("auth", "", "Auth access mode (e.g. JRWP)")
	anon := fs.String("anon", "", "Anon access mode (e.g. N)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	posArgs := fs.Args()
	if len(posArgs) < 1 {
		return nil, fmt.Errorf("chacs requires userid argument")
	}
	userid := posArgs[0]

	cmd := fmt.Sprintf("acc --user=%s --as_root", userid)
	if *auth != "" {
		cmd += fmt.Sprintf(" --auth=%s", *auth)
	}
	if *anon != "" {
		cmd += fmt.Sprintf(" --anon=%s", *anon)
	}

	return []string{cmd}, nil
}

// expandChcred 完成expandChcred所需的内部处理。
func expandChcred(args []string) ([]string, error) {
	fs := flag.NewFlagSet("chcred", flag.ContinueOnError)
	add := fs.String("add", "", "Credential to add (email:alice@example.com)")
	del := fs.String("del", "", "Credential to delete")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	posArgs := fs.Args()
	if len(posArgs) < 1 {
		return nil, fmt.Errorf("chcred requires userid argument")
	}
	userid := posArgs[0]

	cmd := fmt.Sprintf("acc --user=%s --as_root", userid)
	if *add != "" {
		cmd += fmt.Sprintf(" --cred_add=%q", *add)
	}
	if *del != "" {
		cmd += fmt.Sprintf(" --cred_del=%q", *del)
	}

	return []string{cmd}, nil
}

// expandResolve 完成expandResolve所需的内部处理。
func expandResolve(args []string) ([]string, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("resolve requires username argument")
	}
	username := args[0]
	target := username
	if !strings.HasPrefix(username, "usr") {
		target = "usr:" + username
	}
	return []string{fmt.Sprintf(".must $res get %s --desc", target)}, nil
}

// expandThecard 完成expandThecard所需的内部处理。
func expandThecard(args []string) ([]string, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("thecard requires userid argument")
	}
	userid := args[0]
	return []string{fmt.Sprintf(".must $card get %s --desc", userid)}, nil
}
