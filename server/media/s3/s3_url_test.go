package s3

import (
	"testing"

	"chat/server/store/types"
)

func TestGetIdFromURLSupportsCDNObjectPath(t *testing.T) {
	id := types.Uid(901)
	handler := &awshandler{conf: awsconfig{
		ServeURL:   "/v0/file/s/",
		CDNBaseURL: "https://media.example.com",
	}}
	got := handler.GetIdFromUrl(
		"https://media.example.com/chat/files/" + id.String() + "/report.xlsx",
	)
	if got != id {
		t.Fatalf("unexpected CDN file id: %q", got.String())
	}
}

func TestGetIdFromURLRejectsAnotherCDNHost(t *testing.T) {
	id := types.Uid(902)
	handler := &awshandler{conf: awsconfig{
		ServeURL:   "/v0/file/s/",
		CDNBaseURL: "https://media.example.com",
	}}
	got := handler.GetIdFromUrl(
		"https://attacker.example/chat/files/" + id.String() + "/report.xlsx",
	)
	if !got.IsZero() {
		t.Fatalf("unexpected foreign CDN file id: %q", got.String())
	}
}
