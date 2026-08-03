package push

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type alertRoundTripFunc func(*http.Request) (*http.Response, error)

func (function alertRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestDLQAlertWebhookReceivesSanitizedEvent(t *testing.T) {
	received := make(chan dlqAlertEvent, 1)
	runtime := &dlqAlertRuntime{
		config: DLQAlertConfig{
			Enabled: true, WebhookURL: "https://alerts.example.test/hook", BearerToken: "secret",
			TimeoutSeconds: 1, MaxAttempts: 1,
		},
		client: &http.Client{Transport: alertRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("Authorization") != "Bearer secret" {
				t.Errorf("missing authorization header")
			}
			var event dlqAlertEvent
			if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
				t.Errorf("decode event: %v", err)
			}
			received <- event
			return &http.Response{
				StatusCode: http.StatusNoContent, Status: "204 No Content",
				Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header),
			}, nil
		})},
	}
	event := dlqAlertEvent{
		Event: "push_dlq_created", Severity: "critical", Provider: "fcm",
		ID: "job-1", Count: 1, OccurredAt: time.Now().UTC(),
	}
	if err := runtime.deliver(event); err != nil {
		t.Fatal(err)
	}
	got := <-received
	if got.Provider != "fcm" || got.ID != "job-1" || got.Count != 1 {
		t.Fatalf("unexpected event: %+v", got)
	}
}
