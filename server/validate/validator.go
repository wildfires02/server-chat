// Package validate 定义用户凭据校验器（如邮箱验证、短信验证等）必须实现的接口与模板工具函数。
package validate

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"text/template"

	t "chat/server/store/types"
)

// Validator 处理用户凭据校验的统一接口（如邮箱地址、手机号码验证等）。
type Validator interface {
	// Init 初始化凭据校验器。
	Init(jsonconf string) error

	// IsInitialized 返回校验器是否已完成初始化。
	IsInitialized() bool

	// PreCheck 预校验凭据的格式及唯一性，而无需发送实际验证请求。
	// 返回标准化处理后带适当命名空间前缀的凭据字符串（如 "email:alice@example.com"）。
	PreCheck(cred string, params map[string]any) (string, error)

	// Request 向用户发起凭据验证请求（如发送验证码邮件或短信）。
	// 返回 bool 表示是否为新凭据（true 为新凭据，false 表示重新发送未确认的现有凭据）。
	//   user: 发起请求的用户 UID
	//   cred: 正在校验的凭据内容（如邮箱地址或手机号）
	//   lang: Session 中上报的用户语言
	//   resp: 用户已有的可选响应（如验证码/reCAPTCHA）
	//   tmpToken: 包含在请求中的临时身份验证令牌
	Request(user t.Uid, cred, lang, resp string, tmpToken []byte) (bool, error)

	// ResetSecret 发送包含重置密码/密钥说明与链接的消息。
	//   cred: 接收消息的目标地址
	//   scheme: 正在被重置的身份认证方案
	//   lang: 用户语言
	//   tmpToken: 临时认证令牌
	//   params: 认证额外参数
	ResetSecret(cred, scheme, lang string, tmpToken []byte, params map[string]any) error

	// Check 校验用户提交的响应（如验证码）是否正确。
	// 校验成功时返回已验证凭据的规范值。
	Check(user t.Uid, resp string) (string, error)

	// Remove 删除或停用用户的指定凭据。
	Remove(user t.Uid, value string) error

	// Delete 删除指定用户在校验器中的所有记录。
	Delete(user t.Uid) error

	// TempAuthScheme 返回此校验器使用的临时身份认证方案（通常为 "code" 或 "token"）。
	TempAuthScheme() (string, error)
}

// ValidateHostURL 校验并规范化配置中的 host_url 绝对路径。
func ValidateHostURL(origUrl string) (string, error) {
	hostUrl, err := url.Parse(origUrl)
	if err != nil {
		return "", err
	}
	if !hostUrl.IsAbs() {
		return "", errors.New("host_url 必须是绝对路径 (含 http/https 协议)")
	}
	if hostUrl.Hostname() == "" {
		return "", errors.New("无效的 host_url 主机名")
	}
	if hostUrl.Fragment != "" {
		return "", errors.New("host_url 中不允许包含 URL 锚点 fragment")
	}
	if hostUrl.Path == "" {
		hostUrl.Path = "/"
	}
	return hostUrl.String(), nil
}

// ExecuteTemplate 渲染 Go HTML/Text 模板内容。
func ExecuteTemplate(template *template.Template, parts []string, params map[string]any) (map[string]string, error) {
	content := map[string]string{}
	buffer := new(bytes.Buffer)

	if parts == nil {
		if err := template.Execute(buffer, params); err != nil {
			return nil, err
		}
		content[""] = buffer.String()
	} else {
		for _, part := range parts {
			buffer.Reset()
			if templBody := template.Lookup(part); templBody != nil {
				if err := templBody.Execute(buffer, params); err != nil {
					return nil, err
				}
			}
			content[part] = buffer.String()
		}
	}

	return content, nil
}

// ResolveTemplatePath 将相对模板路径解析为绝对文件路径。
func ResolveTemplatePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}

	curwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	return filepath.Clean(filepath.Join(curwd, path)), nil
}

// ReadTemplateFile 根据语言变量读取并解析对应的验证邮件/短信模板文件。
func ReadTemplateFile(pathTempl *template.Template, lang string) (*template.Template, string, error) {
	buffer := bytes.Buffer{}
	err := pathTempl.Execute(&buffer, map[string]any{"Language": lang})
	path := buffer.String()
	if err != nil {
		return nil, path, fmt.Errorf("读取模板文件 %s 失败: %w", path, err)
	}

	templ, err := template.ParseFiles(path)
	return templ, path, err
}
