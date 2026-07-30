package store

import (
	"strings"
	"testing"
)

func TestPersistentJSONCompressionRoundTrip(t *testing.T) {
	input := map[string]any{
		"name":  "contacts",
		"items": strings.Repeat("abcdef", 5000),
	}
	encoded, err := marshalPersistentJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, compressedCachePrefix) || len(encoded) >= len(input["items"].(string)) {
		t.Fatalf("value was not compressed: encoded=%d raw=%d", len(encoded), len(input["items"].(string)))
	}
	var output map[string]any
	if err = unmarshalPersistentJSON(encoded, &output); err != nil {
		t.Fatal(err)
	}
	if output["name"] != input["name"] || output["items"] != input["items"] {
		t.Fatalf("round trip mismatch: %#v", output)
	}
}

func TestPersistentJSONReadsLegacyPlainJSON(t *testing.T) {
	var output map[string]string
	if err := unmarshalPersistentJSON(`{"key":"value"}`, &output); err != nil {
		t.Fatal(err)
	}
	if output["key"] != "value" {
		t.Fatalf("legacy JSON mismatch: %#v", output)
	}
}
