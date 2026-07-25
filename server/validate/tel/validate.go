// Package tel 实现基于短信 (SMS) 或语音验证码的手机号凭据校验器。
package tel

import (
	"crypto/rand"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	textt "text/template"

	"chat/server/logs"
	"chat/server/store"
	t "chat/server/store/types"
	"chat/server/validate"
	"github.com/nyaruka/phonenumbers"
	i18n "golang.org/x/text/language"
)

// validator 手机号校验器结构体。
type validator struct {
	// Web 客户端的基础 URL 地址
	HostUrl string `json:"host_url"`
	// 模板支持的多语言列表
	Languages []string `json:"languages"`
	// 短信验证码及重置密码的通用模板文件路径
	UniversalTemplFile string `json:"universal_templ"`
	// 发信人标识（手机号或签名）
	Sender string `json:"sender"`
	// 调试环境下允许通过的响应码
	DebugResponse string `json:"debug_response"`
	// 最大验证重试次数
	MaxRetries int `json:"max_retries"`
	// 发送的数字验证码长度
	CodeLength int `json:"code_length"`
	// Twilio 服务配置（可选）
	Twilio json.RawMessage `json:"twilio_conf"`

	universalTempl []*textt.Template
	langMatcher    i18n.Matcher
	maxCodeValue   *big.Int
}

const (
	validatorName = "tel"

	defaultMaxRetries = 3

	// 未配置时的默认验证码长度
	defaultCodeLength = 6

	defaultSender = "IM"
)

// Init 初始化手机号校验器。
func (v *validator) Init(jsonconf string) error {
	var err error

	if err = json.Unmarshal([]byte(jsonconf), v); err != nil {
		return err
	}

	if v.HostUrl, err = validate.ValidateHostURL(v.HostUrl); err != nil {
		return err
	}

	var universalPathTempl *textt.Template
	universalPathTempl, err = textt.New("universal").Parse(v.UniversalTemplFile)
	if err != nil {
		return err
	}

	if len(v.Languages) > 0 {
		v.universalTempl = make([]*textt.Template, len(v.Languages))
		var langTags []i18n.Tag
		for idx, lang := range v.Languages {
			tag, err := i18n.Parse(lang)
			if err != nil {
				return err
			}
			langTags = append(langTags, tag)
			if v.universalTempl[idx], _, err = validate.ReadTemplateFile(universalPathTempl, lang); err != nil {
				return err
			}
		}
		v.langMatcher = i18n.NewMatcher(langTags)
	} else {
		v.universalTempl = make([]*textt.Template, 1)
		v.universalTempl[0], _, err = validate.ReadTemplateFile(universalPathTempl, "")
		if err != nil {
			return err
		}
	}

	if v.Twilio != nil {
		if err = twilioInit(v.Twilio); err != nil {
			return err
		}
	}

	if v.Sender == "" {
		v.Sender = defaultSender
	}
	if v.MaxRetries == 0 {
		v.MaxRetries = defaultMaxRetries
	}
	if v.CodeLength == 0 {
		v.CodeLength = defaultCodeLength
	}
	v.maxCodeValue = big.NewInt(0).Exp(big.NewInt(10), big.NewInt(int64(v.CodeLength)), nil)

	return nil
}

// IsInitialized 返回校验器是否已完成初始化。
func (v *validator) IsInitialized() bool {
	return v.CodeLength > 0
}

// PreCheck 预校验手机号格式合法性，并转换为标准的 E.164 格式标签前缀（如 "tel:+16505550100"）。
func (*validator) PreCheck(cred string, params map[string]any) (string, error) {
	if !phonenumbers.VALID_PHONE_NUMBER_PATTERN.MatchString(cred) {
		return "", t.ErrMalformed
	}
	countryCode, ok := params["countryCode"].(string)
	if !ok {
		countryCode = "US"
	}
	number, err := phonenumbers.Parse(cred, countryCode)
	if err != nil {
		return "", t.ErrMalformed
	}
	if !phonenumbers.IsValidNumber(number) {
		return "", t.ErrMalformed
	}
	if numType := phonenumbers.GetNumberType(number); numType != phonenumbers.FIXED_LINE_OR_MOBILE &&
		numType != phonenumbers.MOBILE {
		return "", t.ErrMalformed
	}
	return validatorName + ":" + phonenumbers.Format(number, phonenumbers.E164), nil
}

// Request 生成验证码并通过 SMS 发送给用户，同时在数据库中生成校验记录。
func (v *validator) Request(user t.Uid, phone, lang, resp string, tmpToken []byte) (bool, error) {
	if resp != "" {
		return false, t.ErrFailed
	}

	// 生成随机纯数字验证码
	code, err := rand.Int(rand.Reader, v.maxCodeValue)
	if err != nil {
		return false, err
	}
	resp = strconv.FormatInt(code.Int64(), 10)
	resp = strings.Repeat("0", v.CodeLength-len(resp)) + resp

	var template *textt.Template
	if v.langMatcher != nil {
		_, idx := i18n.MatchStrings(v.langMatcher, lang)
		template = v.universalTempl[idx]
	} else {
		template = v.universalTempl[0]
	}

	content, err := validate.ExecuteTemplate(template, nil, map[string]any{
		"Code":    resp,
		"HostUrl": v.HostUrl})
	if err != nil {
		return false, err
	}

	// 在数据库中插入或更新凭据记录
	isNew, err := store.Users.UpsertCred(&t.Credential{
		User:   user.String(),
		Method: validatorName,
		Value:  phone,
		Resp:   resp})
	if err != nil {
		return false, err
	}

	// 异步非阻塞发送 SMS 短信
	go v.send(phone, content[""])

	return isNew, nil
}

// ResetSecret 发送包含重置密码验证码的短信。
func (v *validator) ResetSecret(phone, scheme, lang string, code []byte, params map[string]any) error {
	var template *textt.Template
	if v.langMatcher != nil {
		_, idx := i18n.MatchStrings(v.langMatcher, lang)
		template = v.universalTempl[idx]
	} else {
		template = v.universalTempl[0]
	}

	content, err := validate.ExecuteTemplate(template, nil, map[string]any{
		"Code":    string(code),
		"HostUrl": v.HostUrl})
	if err != nil {
		return err
	}

	// 异步非阻塞发送 SMS 短信
	go v.send(phone, content[""])

	return nil
}

// Check 校验用户输入的手机验证码。
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
		// 验证码完全正确，确认该手机号
		return cred.Value, store.Users.ConfirmCred(user, validatorName)
	}

	// 验证失败，重试计数加 1
	store.Users.FailCred(user, validatorName)

	return "", t.ErrCredentials
}

// Delete 删除用户的手机号校验记录。
func (*validator) Delete(user t.Uid) error {
	return store.Users.DelCred(user, validatorName, "")
}

// Remove 停用或删除用户的指定手机号。
func (*validator) Remove(user t.Uid, value string) error {
	return store.Users.DelCred(user, validatorName, value)
}

// TempAuthScheme 返回此校验器使用的临时身份认证方案 ("code")。
func (v *validator) TempAuthScheme() (string, error) {
	return "code", nil
}

// send 调用发送接口通过 Twilio 或日志打印发送 SMS。
func (v *validator) send(to, body string) error {
	if v.Twilio != nil {
		if err := twilioSend(v.Sender, to, body); err != nil {
			logs.Warn.Println("Twilio SMS 发信错误", to, err)
		}
	} else {
		logs.Info.Println("发送 SMS, 接收人:", to, "\n短信内容:", body)
	}
	return nil
}

func init() {
	store.RegisterValidator(validatorName, &validator{})
}
