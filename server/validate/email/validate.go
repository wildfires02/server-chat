// Package email 实现基于外部 SMTP 发信服务器的邮箱凭据校验器。
package email

import (
	"bytes"
	crand "crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"mime"
	qp "mime/quotedprintable"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	textt "text/template"

	"slices"

	"chat/server/logs"
	"chat/server/store"
	t "chat/server/store/types"
	"chat/server/validate"
	i18n "golang.org/x/text/language"
)

// validator 邮箱校验器配置结构体。
type validator struct {
	// Web 客户端的基础 URL 地址
	HostUrl string `json:"host_url"`
	// 模板支持的多语言列表
	Languages []string `json:"languages"`
	// 邮箱验证模板文件路径
	ValidationTemplFile string `json:"validation_templ"`
	// 重置密码模板文件路径
	ResetTemplFile string `json:"reset_secret_templ"`
	// 发件人 RFC 5322 邮箱地址
	SendFrom string `json:"sender"`
	// SMTP 认证账号
	Login string `json:"login"`
	// SMTP 认证密码
	SenderPassword string `json:"sender_password"`
	// SMTP 认证机制（可选: "login", "cram-md5", "plain"，默认 "plain"）
	AuthMechanism string `json:"auth_mechanism"`
	// 可选的调试响应，用于跳过实际校验
	DebugResponse string `json:"debug_response"`
	// 邮箱锁定前的最大尝试验证次数
	MaxRetries int `json:"max_retries"`
	// SMTP 服务器地址
	SMTPAddr string `json:"smtp_server"`
	// SMTP 服务器端口
	SMTPPort string `json:"smtp_port"`
	// SMTP HELO/EHLO 命令中使用的主机名
	SMTPHeloHost string `json:"smtp_helo_host"`
	// 跳过服务器证书链与主机名的安全校验（不安全模式）
	TLSInsecureSkipVerify bool `json:"insecure_skip_verify"`
	// 允许注册的邮箱域名白名单（可选）
	Domains []string `json:"domains"`
	// 发送的验证码数字位数长度
	CodeLength int `json:"code_length"`

	validationTempl []*textt.Template
	resetTempl      []*textt.Template
	auth            smtp.Auth
	senderEmail     string
	langMatcher     i18n.Matcher
	maxCodeValue    *big.Int
}

const (
	validatorName = "email"

	defaultMaxRetries = 3
	defaultPort       = "25"

	// 邮箱地址最大安全字节长度
	maxEmailLength = 128

	// 未配置时的默认验证码长度
	defaultCodeLength = 6
)

// 邮件模板包含的段落组件
var templateParts = []string{"subject", "body_plain", "body_html"}

// Init 初始化邮箱校验器。
func (v *validator) Init(jsonconf string) error {
	if err := json.Unmarshal([]byte(jsonconf), v); err != nil {
		return err
	}

	sender, err := mail.ParseAddress(v.SendFrom)
	if err != nil {
		return err
	}
	v.senderEmail = sender.Address

	// 如果配置了登录用户名，则启用 SMTP 认证
	if v.Login != "" {
		mechanism := strings.ToLower(v.AuthMechanism)
		switch mechanism {
		case "cram-md5":
			v.auth = smtp.CRAMMD5Auth(v.Login, v.SenderPassword)
		case "login":
			v.auth = &loginAuth{[]byte(v.Login), []byte(v.SenderPassword)}
		case "", "plain":
			v.auth = smtp.PlainAuth("", v.Login, v.SenderPassword, v.SMTPAddr)
		default:
			return errors.New("未知的 auth_mechanism 认证机制")
		}
	}

	// 解析模板绝对路径
	v.ValidationTemplFile, err = validate.ResolveTemplatePath(v.ValidationTemplFile)
	if err != nil {
		return err
	}
	v.ResetTemplFile, err = validate.ResolveTemplatePath(v.ResetTemplFile)
	if err != nil {
		return err
	}

	var validationPathTempl, resetPathTempl *textt.Template
	validationPathTempl, err = textt.New("validation").Parse(v.ValidationTemplFile)
	if err != nil {
		return err
	}
	resetPathTempl, err = textt.New("reset").Parse(v.ResetTemplFile)
	if err != nil {
		return err
	}

	var path string
	if len(v.Languages) > 0 {
		v.validationTempl = make([]*textt.Template, len(v.Languages))
		v.resetTempl = make([]*textt.Template, len(v.Languages))
		var langTags []i18n.Tag
		for idx, lang := range v.Languages {
			tag, err := i18n.Parse(lang)
			if err != nil {
				return err
			}
			langTags = append(langTags, tag)
			if v.validationTempl[idx], path, err = validate.ReadTemplateFile(validationPathTempl, lang); err != nil {
				return err
			}
			if err = isTemplateValid(v.validationTempl[idx]); err != nil {
				return fmt.Errorf("解析模板文件 %s 失败: %w", path, err)
			}

			if v.resetTempl[idx], path, err = validate.ReadTemplateFile(resetPathTempl, lang); err != nil {
				return err
			}
			if err = isTemplateValid(v.resetTempl[idx]); err != nil {
				return fmt.Errorf("解析模板文件 %s 失败: %w", path, err)
			}
		}
		v.langMatcher = i18n.NewMatcher(langTags)
	} else {
		v.validationTempl = make([]*textt.Template, 1)
		v.resetTempl = make([]*textt.Template, 1)
		v.validationTempl[0], path, err = validate.ReadTemplateFile(validationPathTempl, "")
		if err != nil {
			return err
		}
		if err = isTemplateValid(v.validationTempl[0]); err != nil {
			return fmt.Errorf("解析模板文件 %s 失败: %w", path, err)
		}

		v.resetTempl[0], path, err = validate.ReadTemplateFile(resetPathTempl, "")
		if err != nil {
			return err
		}
		if err = isTemplateValid(v.resetTempl[0]); err != nil {
			return fmt.Errorf("解析模板文件 %s 失败: %w", path, err)
		}
	}

	if v.HostUrl, err = validate.ValidateHostURL(v.HostUrl); err != nil {
		return err
	}

	if v.SMTPHeloHost == "" {
		hostUrl, _ := url.Parse(v.HostUrl)
		v.SMTPHeloHost = hostUrl.Hostname()
	}
	if v.SMTPHeloHost == "" {
		return errors.New("缺少 SMTP 主机名配置 (smtp_helo_host)")
	}

	if v.MaxRetries == 0 {
		v.MaxRetries = defaultMaxRetries
	}
	if v.CodeLength == 0 {
		v.CodeLength = defaultCodeLength
	}
	v.maxCodeValue = big.NewInt(0).Exp(big.NewInt(10), big.NewInt(int64(v.CodeLength)), nil)

	if v.SMTPPort == "" {
		v.SMTPPort = defaultPort
	}

	return nil
}

// IsInitialized 返回校验器是否已完成初始化。
func (v *validator) IsInitialized() bool {
	return v.SMTPHeloHost != ""
}

// PreCheck 预校验邮箱地址格式与域名白名单（无需发送邮件）。
func (v *validator) PreCheck(cred string, _ map[string]any) (string, error) {
	if len(cred) > maxEmailLength {
		return "", t.ErrMalformed
	}

	// 校验是否为合法的 user@domain 邮箱格式
	addr, err := mail.ParseAddress(cred)
	if err != nil || addr.Address != cred {
		return "", t.ErrMalformed
	}

	// 归一化为小写，防止大小写冲突
	addr.Address = strings.ToLower(addr.Address)

	// 如果配置了域名白名单，校验邮箱后缀域名
	if len(v.Domains) > 0 {
		parts := strings.Split(addr.Address, "@")
		if len(parts) != 2 {
			return "", t.ErrMalformed
		}

		if !slices.Contains(v.Domains, parts[1]) {
			return "", t.ErrPolicy
		}
	}

	return validatorName + ":" + addr.Address, nil
}

// Request 发送验证码邮件并向数据库写入凭据校验记录。
func (v *validator) Request(user t.Uid, email, lang, resp string, tmpToken []byte) (bool, error) {
	if resp != "" {
		return false, t.ErrFailed
	}

	email = strings.ToLower(email)

	token := make([]byte, base64.StdEncoding.EncodedLen(len(tmpToken)))
	base64.StdEncoding.Encode(token, tmpToken)

	// 随机生成固定长度的纯数字验证码
	code, err := crand.Int(crand.Reader, v.maxCodeValue)
	if err != nil {
		return false, err
	}
	resp = strconv.FormatInt(code.Int64(), 10)
	resp = strings.Repeat("0", v.CodeLength-len(resp)) + resp

	var template *textt.Template
	if v.langMatcher != nil {
		normalized, _ := i18n.Parse(lang)
		_, idx := i18n.MatchStrings(v.langMatcher, normalized.String())
		template = v.validationTempl[idx]
	} else {
		template = v.validationTempl[0]
	}

	content, err := validate.ExecuteTemplate(template, templateParts, map[string]any{
		"Token":   url.QueryEscape(string(token)),
		"Code":    resp,
		"HostUrl": v.HostUrl})
	if err != nil {
		return false, err
	}

	// 在数据库中更新或插入凭据校验记录
	isNew, err := store.Users.UpsertCred(&t.Credential{
		User:   user.String(),
		Method: validatorName,
		Value:  email,
		Resp:   resp})
	if err != nil {
		return false, err
	}

	// 异步非阻塞发送邮件
	go func() {
		if sendErr := v.send(email, content); sendErr != nil {
			logs.Warn.Println("邮箱异步发送失败:", email, sendErr)
		}
	}()

	return isNew, nil
}

// ResetSecret 发送重置密码邮件说明及验证码。
func (v *validator) ResetSecret(email, scheme, lang string, code []byte, params map[string]any) error {
	email = strings.ToLower(email)

	var template *textt.Template
	if v.langMatcher != nil {
		_, idx := i18n.MatchStrings(v.langMatcher, lang)
		template = v.resetTempl[idx]
	} else {
		template = v.resetTempl[0]
	}

	var login string
	if params != nil {
		if l, ok := params["login"].(string); ok {
			login = l
		}
	}

	content, err := validate.ExecuteTemplate(template, templateParts, map[string]any{
		"Login":   login,
		"Code":    string(code),
		"Cred":    email,
		"Scheme":  scheme,
		"HostUrl": v.HostUrl})
	if err != nil {
		return err
	}

	// 异步非阻塞发送邮件
	go func() {
		if sendErr := v.send(email, content); sendErr != nil {
			logs.Warn.Println("重置密码邮件异步发送失败:", email, sendErr)
		}
	}()

	return nil
}

// Check 校验用户输入的验证码是否与数据库记录一致。
func (v *validator) Check(user t.Uid, resp string) (string, error) {
	cred, err := store.Users.GetActiveCred(user, validatorName)
	if err != nil {
		return "", err
	}

	if cred == nil {
		return "", t.ErrNotFound
	}

	if cred.Retries > v.MaxRetries {
		return "", t.ErrPolicy
	}

	if resp == "" {
		return "", t.ErrCredentials
	}

	if cred.Resp == resp || v.DebugResponse == resp {
		// 验证码匹配成功，确认凭据有效
		return cred.Value, store.Users.ConfirmCred(user, validatorName)
	}

	// 验证失败，重试计数加 1
	_ = store.Users.FailCred(user, validatorName)

	return "", t.ErrCredentials
}

// Delete 删除用户的邮箱校验记录。
func (v *validator) Delete(user t.Uid) error {
	return store.Users.DelCred(user, validatorName, "")
}

// Remove 停用或删除用户的指定邮箱。
func (v *validator) Remove(user t.Uid, value string) error {
	return store.Users.DelCred(user, validatorName, value)
}

// TempAuthScheme 返回此校验器使用的临时身份认证方案 ("code")。
func (v *validator) TempAuthScheme() (string, error) {
	return "code", nil
}

// sendMail 执行 SMTP 握手、STARTTLS 加密与发信。
func (v *validator) sendMail(rcpt []string, msg []byte) error {
	client, err := smtp.Dial(v.SMTPAddr + ":" + v.SMTPPort)
	if err != nil {
		return err
	}
	defer func() {
		_ = client.Close()
	}()
	if err = client.Hello(v.SMTPHeloHost); err != nil {
		return err
	}
	if istls, _ := client.Extension("STARTTLS"); istls {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: v.TLSInsecureSkipVerify,
			ServerName:         v.SMTPAddr,
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if v.auth != nil {
		if isauth, _ := client.Extension("AUTH"); isauth {
			err = client.Auth(v.auth)
			if err != nil {
				return err
			}
		}
	}
	if err = client.Mail(strings.ReplaceAll(strings.ReplaceAll(v.senderEmail, "\r", " "), "\n", " ")); err != nil {
		return err
	}
	for _, to := range rcpt {
		if err = client.Rcpt(strings.ReplaceAll(strings.ReplaceAll(to, "\r", " "), "\n", " ")); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err = w.Write(msg); err != nil {
		_ = w.Close()
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// send 组装 MIME 邮件报文并通过 SMTP 发送邮件。
func (v *validator) send(to string, content map[string]string) error {
	message := &bytes.Buffer{}

	_, _ = fmt.Fprintf(message, "From: %s\r\n", v.SendFrom)
	_, _ = fmt.Fprintf(message, "To: %s\r\n", to)
	_, _ = message.WriteString("Subject: ")
	_, _ = message.WriteString(strings.Join(strings.Split(mime.QEncoding.Encode("utf-8", content["subject"]), " "), "\r\n    "))
	_, _ = message.WriteString("\r\n")
	_, _ = message.WriteString("MIME-version: 1.0;\r\n")

	if content["body_html"] == "" {
		_, _ = message.WriteString("Content-Type: text/plain; charset=\"UTF-8\"; format=flowed; delsp=yes\r\n")
		_, _ = message.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b64w := base64.NewEncoder(base64.StdEncoding, message)
		_, _ = b64w.Write([]byte(content["body_plain"]))
		_ = b64w.Close()
	} else if content["body_plain"] == "" {
		_, _ = message.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
		_, _ = message.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		qpw := qp.NewWriter(message)
		_, _ = qpw.Write([]byte(content["body_html"]))
		_ = qpw.Close()
	} else {
		boundary := randomBoundary()
		_, _ = message.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")

		_, _ = message.WriteString("--" + boundary + "\r\n")
		_, _ = message.WriteString("Content-Type: text/plain; charset=\"UTF-8\"; format=flowed; delsp=yes\r\n")
		_, _ = message.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b64w := base64.NewEncoder(base64.StdEncoding, message)
		_, _ = b64w.Write([]byte(content["body_plain"]))
		_ = b64w.Close()

		_, _ = message.WriteString("\r\n")

		_, _ = message.WriteString("--" + boundary + "\r\n")
		_, _ = message.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
		_, _ = message.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		qpw := qp.NewWriter(message)
		_, _ = qpw.Write([]byte(content["body_html"]))
		_ = qpw.Close()

		_, _ = message.WriteString("\r\n--" + boundary + "--")
	}

	_, _ = message.WriteString("\r\n")

	err := v.sendMail([]string{to}, message.Bytes())
	if err != nil {
		logs.Warn.Println("SMTP 发信错误", to, err)
	}

	return err
}

func isTemplateValid(templ *textt.Template) error {
	if templ.Lookup("subject") == nil {
		return fmt.Errorf("模板无效: 未找到 '%s'", "subject")
	}
	if templ.Lookup("body_plain") == nil && templ.Lookup("body_html") == nil {
		return fmt.Errorf("模板无效: 未找到 '%s' 或 '%s'", "body_plain", "body_html")
	}
	return nil
}

type loginAuth struct {
	username, password []byte
}

func (a *loginAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", []byte(a.username), nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		switch strings.ToLower(string(fromServer)) {
		case "username:":
			return a.username, nil
		case "password:":
			return a.password, nil
		default:
			return nil, fmt.Errorf("LOGIN AUTH 未知服务器响应 '%s'", string(fromServer))
		}
	}
	return nil, nil
}

func randomBoundary() string {
	var buf [24]byte
	_, _ = crand.Read(buf[:])
	return fmt.Sprintf("im--%x", buf[:])
}

func init() {
	store.RegisterValidator(validatorName, &validator{})
}
