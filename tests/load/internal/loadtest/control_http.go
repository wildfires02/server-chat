package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

func controlRequest(
	ctx context.Context,
	client *http.Client,
	method string,
	endpoint string,
	token string,
	requestBody any,
	responseBody any,
) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		errorBody, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &controlResponseError{
			statusCode: response.StatusCode,
			body:       string(errorBody),
		}
	}
	if responseBody == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(responseBody)
}

type controlResponseError struct {
	statusCode int
	body       string
}

func (controlErr *controlResponseError) Error() string {
	return fmt.Sprintf(
		"控制器状态=%d 响应=%q",
		controlErr.statusCode,
		controlErr.body,
	)
}

func isPermanentControlError(err error) bool {
	var responseErr *controlResponseError
	return errors.As(err, &responseErr) &&
		responseErr.statusCode >= http.StatusBadRequest &&
		responseErr.statusCode < http.StatusInternalServerError
}

func decodeControlJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 16<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("请求 JSON 无效: %w", err)
	}
	return nil
}

func writeControlJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeControlError(writer http.ResponseWriter, status int, message string) {
	writeControlJSON(writer, status, map[string]any{
		"error":  http.StatusText(status),
		"detail": message,
	})
}
