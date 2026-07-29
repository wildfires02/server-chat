// Package tel 实现即时通信服务端的协议、路由和业务逻辑。
package tel

import (
	"encoding/json"

	"github.com/twilio/twilio-go"
	twapi "github.com/twilio/twilio-go/rest/api/v2010"
)

// twilioConfig 保存twilio配置的数据和运行状态。
type twilioConfig struct {
	// AccountSid 保存AccountSid。
	AccountSid string `json:"account_sid"`
	// AuthToken 保存认证令牌。
	AuthToken string `json:"auth_token"`
}

// twilioClient 保存twilio客户端的共享实例或运行状态。
var twilioClient *twilio.RestClient

// twilioInit 完成twilioInit所需的内部处理。
func twilioInit(jsonconf json.RawMessage) error {
	var conf twilioConfig

	if err := json.Unmarshal(jsonconf, &conf); err != nil {
		return err
	}

	twilioClient = twilio.NewRestClientWithParams(twilio.ClientParams{
		Username: conf.AccountSid,
		Password: conf.AuthToken,
	})

	return nil
}

// twilioSend 完成twilioSend所需的内部处理。
func twilioSend(from, to, body string) error {
	_, err := twilioClient.Api.CreateMessage(&twapi.CreateMessageParams{
		From: &from,
		To:   &to,
		Body: &body,
	})
	return err
}
