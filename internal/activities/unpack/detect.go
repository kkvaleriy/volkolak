package unpack

import (
	"archive/tar"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kkvaleriy/volkolak/internal/actlog"
	"github.com/kkvaleriy/volkolak/internal/fail"
)

// detectFile определяет тип архива по сигнатуре, а не по расширению.
// Расширение используется только если сигнатуры нет (старый tar без ustar).
func detectFile(ctx context.Context, filePath string) (string, error) {
	actlog.Info(ctx, "определяю тип архива", "file", filePath)

	info, err := os.Stat(filePath)
	if err != nil {
		return "", fail.Input(fmt.Sprintf("архив недоступен: %v", err))
	}
	if !info.Mode().IsRegular() {
		return "", fail.Input("архив должен быть обычным файлом")
	}

	f, err := os.Open(filePath)
	if err != nil {
		return "", fail.Input(fmt.Sprintf("архив недоступен: %v", err))
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, readErr := f.Read(buf)
	if n == 0 {
		if readErr == nil {
			readErr = io.EOF
		}
		return "", fail.Input(fmt.Sprintf("пустой или нечитаемый архив: %v", readErr))
	}
	header := buf[:n]

	switch {
	case hasPrefix(header, []byte{0x50, 0x4B, 0x03, 0x04}) ||
		hasPrefix(header, []byte{0x50, 0x4B, 0x05, 0x06}) ||
		hasPrefix(header, []byte{0x50, 0x4B, 0x07, 0x08}):
		return "zip", nil
	case hasPrefix(header, []byte{0x1F, 0x8B}):
		return gzipKind(filePath)
	case hasPrefix(header, []byte{'B', 'Z', 'h'}):
		return bzip2Kind(filePath), nil
	case hasPrefix(header, []byte{0xFD, '7', 'z', 'X', 'Z', 0x00}):
		return "xz", nil
	case hasPrefix(header, []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}):
		return "7z", nil
	case hasPrefix(header, []byte{'R', 'a', 'r', '!'}):
		return "rar", nil
	case len(header) >= 262 && string(header[257:262]) == "ustar":
		return "tar", nil
	default:
		switch filepath.Ext(filePath) {
		case ".zip":
			return "zip", nil
		case ".tar":
			return "tar", nil
		case ".tgz":
			return "tar.gz", nil
		case ".gz":
			return "gzip", nil
		case ".bz2":
			return "bzip2", nil
		case ".xz":
			return "xz", nil
		case ".7z":
			return "7z", nil
		case ".rar":
			return "rar", nil
		default:
			return "", fail.Input("неизвестный тип архива")
		}
	}
}

func hasPrefix(header, magic []byte) bool {
	if len(header) < len(magic) {
		return false
	}
	for i := range magic {
		if header[i] != magic[i] {
			return false
		}
	}
	return true
}

func gzipKind(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fail.Input(fmt.Sprintf("gzip недоступен: %v", err))
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fail.Input(fmt.Sprintf("повреждённый gzip: %v", err))
	}
	defer gz.Close()

	if isTar(gz) {
		return "tar.gz", nil
	}
	return "gzip", nil
}

func bzip2Kind(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "bzip2"
	}
	defer f.Close()
	if isTar(bzip2.NewReader(f)) {
		return "tar.bz2"
	}
	return "bzip2"
}

func isTar(r io.Reader) bool {
	tr := tar.NewReader(r)
	_, err := tr.Next()
	return err == nil || err == io.EOF
}
