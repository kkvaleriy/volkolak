package transcribe

import "testing"

func TestParseWhisper(t *testing.T) {
	raw := []byte(`{
		"result": {"language": "ru"},
		"transcription": [
			{"offsets": {"from": 0, "to": 1200}, "text": " Привет"},
			{"offsets": {"from": 1200, "to": 2400}, "text": " "},
			{"offsets": {"from": 2400, "to": 4000}, "text": "это тест"}
		]
	}`)
	got, err := parseWhisper(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != "ru" {
		t.Fatalf("language %q", got.Language)
	}
	if got.Text != "Привет\nэто тест" {
		t.Fatalf("text %q", got.Text)
	}
	if len(got.Segments) != 2 || got.Segments[1].StartMs != 2400 {
		t.Fatalf("segments %#v", got.Segments)
	}
}
