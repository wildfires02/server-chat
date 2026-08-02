package fcm

import (
	"encoding/json"
	"testing"

	"chat/server/push/common"
)

func TestAndroidCallPushUsesDataOnlyPayload(t *testing.T) {
	config := &configType{}
	android := androidNotificationConfig("msg", "usr-peer", map[string]string{
		"webrtc": "started",
		"aonly":  "true",
	}, config)
	if android == nil {
		t.Fatal("expected Android call configuration")
	}
	if android.Notification != nil {
		t.Fatal("Android call must be data-only so the Flutter background handler can build the native call notification")
	}
	if android.Priority != string(common.AndroidPriorityHigh) || android.Ttl != "0s" {
		t.Fatalf("unexpected Android call delivery settings: priority=%q ttl=%q", android.Priority, android.Ttl)
	}
}

func TestApnsCallPushUsesStandardTimeSensitiveAlert(t *testing.T) {
	config := &configType{ApnsBundleID: "com.example.mall"}
	apns := apnsNotificationConfig("msg", "usr-peer", map[string]string{
		"webrtc": "started",
		"aonly":  "true",
		"xfrom":  "Agent 7",
	}, 0, config)
	if apns == nil {
		t.Fatal("expected APNS call configuration")
	}
	if got := apns.Headers[common.HeaderApnsPushType]; got != string(common.ApnsPushTypeAlert) {
		t.Fatalf("call push type=%q, want alert", got)
	}
	if got := apns.Headers[common.HeaderApnsTopic]; got != config.ApnsBundleID {
		t.Fatalf("APNS topic=%q, want %q", got, config.ApnsBundleID)
	}

	var payload struct {
		Aps struct {
			InterruptionLevel string `json:"interruption-level"`
			Alert             struct {
				Title string `json:"title"`
				Body  string `json:"body"`
			} `json:"alert"`
		} `json:"aps"`
	}
	if err := json.Unmarshal(apns.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Aps.InterruptionLevel != string(common.InterruptionLevelTimeSensitive) {
		t.Fatalf("interruption level=%q, want time-sensitive", payload.Aps.InterruptionLevel)
	}
	if payload.Aps.Alert.Title != "Incoming voice call" || payload.Aps.Alert.Body != "Agent 7 is calling" {
		t.Fatalf("unexpected APNS call alert: %+v", payload.Aps.Alert)
	}
}
