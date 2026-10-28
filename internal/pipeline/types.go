package pipeline

import "strings"

// VideoFile — видео, оставшееся после распаковки.
type VideoFile struct {
	Key          string `json:"key"`
	RelativePath string `json:"relative_path"`
	Filename     string `json:"filename"`
}

// Frame — один кадр, вырезанный из видео.
type Frame struct {
	Index        int     `json:"index"`
	TimestampSec float64 `json:"timestamp_sec"`
	Key          string  `json:"key"`
}

// FrameAnalysis — текст и описание одного кадра.
type FrameAnalysis struct {
	Index        int     `json:"index"`
	TimestampSec float64 `json:"timestamp_sec"`
	Key          string  `json:"key"`
	OCRText      string  `json:"ocr_text"`
	Description  string  `json:"description"`
	Error        string  `json:"error"`
}

// Segment — фрагмент расшифровки с таймкодами whisper.
type Segment struct {
	StartMs int    `json:"start_ms"`
	EndMs   int    `json:"end_ms"`
	Text    string `json:"text"`
}

// Transcript — результат распознавания речи.
type Transcript struct {
	Language string    `json:"language"`
	Text     string    `json:"text"`
	Segments []Segment `json:"segments"`
	HasAudio bool      `json:"has_audio"`
}

// Clip обрезает строку по числу рун. Вторая величина true, если строка укорочена.
func Clip(s string, maxRunes int) (string, bool) {
	s = strings.TrimSpace(s)
	if maxRunes <= 0 {
		return "", s != ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s, false
	}
	return string(runes[:maxRunes]), true
}
