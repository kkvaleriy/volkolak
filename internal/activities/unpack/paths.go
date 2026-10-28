package unpack

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// cleanArchivePath приводит имя из архива к относительному пути.
// Пустое имя означает «пропустить запись», ошибка — обрыв распаковки.
func cleanArchivePath(name string) (string, error) {
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("недопустимое имя %q", name)
	}
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Clean(name)
	if name == "." || name == "" {
		return "", nil
	}
	if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("выход за каталог назначения: %q", name)
	}
	return name, nil
}

func isSafePath(base, target string) bool {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func skipNoise(name string) bool {
	slash := strings.ReplaceAll(name, "\\", "/")
	if slash == "__MACOSX" || strings.HasPrefix(slash, "__MACOSX/") || strings.Contains(slash, "/__MACOSX/") {
		return true
	}
	base := path.Base(slash)
	return base == ".DS_Store" || strings.HasPrefix(base, "._")
}
