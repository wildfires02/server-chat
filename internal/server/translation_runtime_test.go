package server

import (
	"testing"

	translation "chat/server/translate"
)

type staticTranslationSettingsSource struct {
	snapshot translationSettingsSnapshot
}

func (source staticTranslationSettingsSource) Snapshot() (translationSettingsSnapshot, error) {
	return source.snapshot, nil
}

func TestTranslationDirection(t *testing.T) {
	settings := translation.Settings{
		StaffLanguage: "zh-CN", CustomerLanguage: "en",
	}
	tests := []struct {
		source   string
		receiver string
		want     string
	}{
		{source: "zh", receiver: "", want: "en"},
		{source: "en-US", receiver: "", want: "zh-CN"},
		{source: "fr", receiver: "de-DE", want: "de-DE"},
	}
	for _, test := range tests {
		if got := translationTarget(settings, test.source, test.receiver); got != test.want {
			t.Errorf("translationTarget(%q, %q)=%q, want %q",
				test.source, test.receiver, got, test.want)
		}
	}
}

func TestLikelyLanguage(t *testing.T) {
	if got := likelyLanguage("你好，订单已发货"); got != "zh" {
		t.Fatalf("Chinese detection=%q", got)
	}
	if got := likelyLanguage("Your order has shipped"); got != "en" {
		t.Fatalf("English detection=%q", got)
	}
	if got := likelyLanguage("🎉"); got != "auto" {
		t.Fatalf("unknown detection=%q", got)
	}
}

func TestTranslationDataDoesNotExposeOriginalUnlessEnabled(t *testing.T) {
	data := &MsgServerData{Content: "你好", SeqId: 10, Kind: "text"}
	entry := cachedTranslation{
		Text: "Hello", DetectedSourceLanguage: "zh",
		TargetLanguage: "en", Provider: "primary",
	}
	hidden := completedTranslationData(data, "你好", false, entry)
	if hidden.Content != "Hello" || hidden.Translation.Original != nil {
		t.Fatalf("unexpected hidden-original projection: %#v", hidden)
	}
	visible := completedTranslationData(data, "你好", true, entry)
	if visible.Translation.Original != "你好" {
		t.Fatalf("original not retained: %#v", visible)
	}
}

func TestTranslationFailsClosedBeforeAdminPolicyIsAvailable(t *testing.T) {
	settings := translation.Settings{
		Enabled: true, StaffLanguage: "zh-CN", CustomerLanguage: "en",
		KeepOriginal: true, FailurePolicy: "hold",
	}
	translation.NormalizeSettings(&settings)
	runtime := &translationRuntime{
		source: staticTranslationSettingsSource{
			snapshot: translationSettingsSnapshot{Settings: settings},
		},
		cache: make(map[string]cachedTranslation),
	}
	projected, start := runtime.project("usrAusrB",
		&MsgServerData{Content: "你好", SeqId: 10, Kind: "text"},
		"", false, nil)
	if start != nil {
		t.Fatal("unconfigured translation must not enqueue a provider call")
	}
	if projected.Content != nil || projected.Translation == nil ||
		projected.Translation.Status != "failed" {
		t.Fatalf("unconfigured translation leaked content: %#v", projected)
	}
}
