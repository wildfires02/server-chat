package loadtest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

func (client *websocketClient) handshake(ctx context.Context, protocolVersion string) error {
	requestID := client.nextRequestID("hi")
	control, err := client.requestControl(ctx, requestID, map[string]any{
		"hi": map[string]any{
			"id":  requestID,
			"ver": protocolVersion,
			"ua":  "im-load/1.0",
		},
	})
	if err != nil {
		return err
	}
	if !successfulControl(control.Code) {
		return controlError(control)
	}
	return nil
}

func (client *websocketClient) login(
	ctx context.Context,
	account Account,
	cache *tokenCache,
) error {
	token := account.Token
	if token == "" {
		token = cache.Load(account.Username)
	}
	scheme := "token"
	secret := token
	if secret == "" {
		scheme = "basic"
		secret = base64.StdEncoding.EncodeToString(
			[]byte(account.Username + ":" + account.Password),
		)
	}

	requestID := client.nextRequestID("login")
	control, err := client.requestControl(ctx, requestID, map[string]any{
		"login": map[string]any{
			"id":     requestID,
			"scheme": scheme,
			"secret": secret,
		},
	})
	if err != nil {
		return err
	}
	if !successfulControl(control.Code) {
		return controlError(control)
	}
	if rawToken := control.Params["token"]; len(rawToken) > 0 {
		var issuedToken string
		if json.Unmarshal(rawToken, &issuedToken) == nil {
			cache.Store(account.Username, issuedToken)
		}
	}
	return nil
}

func (client *websocketClient) subscribe(ctx context.Context, topic string) error {
	requestID := client.nextRequestID("sub")
	control, err := client.requestControl(ctx, requestID, map[string]any{
		"sub": map[string]any{
			"id":    requestID,
			"topic": topic,
		},
	})
	if err != nil {
		return err
	}
	if !successfulControl(control.Code) {
		return controlError(control)
	}
	return nil
}

func (client *websocketClient) subscriptions(ctx context.Context) ([]string, error) {
	requestID := client.nextRequestID("get-subs")
	meta, err := client.requestMeta(ctx, requestID, map[string]any{
		"get": map[string]any{
			"id":    requestID,
			"topic": "me",
			"what":  "sub",
		},
	})
	if err != nil {
		return nil, err
	}
	topics := make([]string, 0, len(meta.Sub))
	for _, subscription := range meta.Sub {
		if subscription.Topic != "" {
			topics = append(topics, subscription.Topic)
		}
	}
	return topics, nil
}

func (client *websocketClient) publish(
	ctx context.Context,
	topic string,
	clientID string,
	content loadMessageContent,
) error {
	requestID := client.nextRequestID("pub")
	control, err := client.requestControl(ctx, requestID, map[string]any{
		"pub": map[string]any{
			"id":      requestID,
			"topic":   topic,
			"cid":     clientID,
			"noecho":  true,
			"kind":    "text",
			"content": content,
		},
	})
	if err != nil {
		return err
	}
	if !successfulControl(control.Code) {
		return controlError(control)
	}
	return nil
}

func (client *websocketClient) leave(ctx context.Context, topic string) error {
	requestID := client.nextRequestID("leave")
	control, err := client.requestControl(ctx, requestID, map[string]any{
		"leave": map[string]any{
			"id":    requestID,
			"topic": topic,
		},
	})
	if err != nil {
		return err
	}
	if !successfulControl(control.Code) {
		return controlError(control)
	}
	return nil
}

func successfulControl(code int) bool {
	return code >= http.StatusOK && code < http.StatusBadRequest
}

func controlError(control *wireControl) error {
	if control == nil {
		return errors.New("服务端返回空控制响应")
	}
	return fmt.Errorf("服务端状态码=%d 文本=%q", control.Code, control.Text)
}
