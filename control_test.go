package claudecli

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestTextBlock(t *testing.T) {
	b := TextBlock("hello world")
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["type"] != "text" {
		t.Errorf("type = %v, want text", parsed["type"])
	}
	if parsed["text"] != "hello world" {
		t.Errorf("text = %v, want 'hello world'", parsed["text"])
	}
}

func TestImageBlock(t *testing.T) {
	raw := []byte{0x89, 0x50, 0x4E, 0x47} // PNG magic
	b := ImageBlock("image/png", raw)
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["type"] != "image" {
		t.Errorf("type = %v, want image", parsed["type"])
	}
	source := parsed["source"].(map[string]any)
	if source["type"] != "base64" {
		t.Errorf("source.type = %v, want base64", source["type"])
	}
	if source["media_type"] != "image/png" {
		t.Errorf("source.media_type = %v, want image/png", source["media_type"])
	}
	decoded, err := base64.StdEncoding.DecodeString(source["data"].(string))
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if len(decoded) != len(raw) {
		t.Errorf("decoded length = %d, want %d", len(decoded), len(raw))
	}
}

func TestDocumentBlock(t *testing.T) {
	raw := []byte{0x25, 0x50, 0x44, 0x46} // %PDF
	b := DocumentBlock("application/pdf", raw)
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["type"] != "document" {
		t.Errorf("type = %v, want document", parsed["type"])
	}
	source := parsed["source"].(map[string]any)
	if source["type"] != "base64" {
		t.Errorf("source.type = %v, want base64", source["type"])
	}
	if source["media_type"] != "application/pdf" {
		t.Errorf("source.media_type = %v, want application/pdf", source["media_type"])
	}
}

func TestContentBlockMarshalJSON(t *testing.T) {
	b := TextBlock("test")
	data, err := b.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("MarshalJSON returned empty")
	}
	// Round-trip: should be valid JSON
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("MarshalJSON produced invalid JSON: %v", err)
	}
}
