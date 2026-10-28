package unpack

import "strings"

// diskFile — видео, оставшееся на диске после локальной распаковки.
type diskFile struct {
	Path         string
	RelativePath string
	Filename     string
}

func isVideo(name string) bool {
	switch strings.ToLower(pathExt(name)) {
	case ".mp4", ".m4v", ".mkv", ".webm", ".mov", ".avi", ".mpeg", ".mpg",
		".wmv", ".flv", ".ts", ".mts", ".m2ts", ".3gp", ".ogv":
		return true
	default:
		return false
	}
}

func pathExt(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	dot := strings.LastIndex(name, ".")
	slash := strings.LastIndex(name, "/")
	if dot <= slash || dot < 0 {
		return ""
	}
	return name[dot:]
}
