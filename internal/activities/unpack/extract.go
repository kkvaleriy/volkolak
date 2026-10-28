package unpack

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kkvaleriy/volkolak/internal/actlog"
	"github.com/kkvaleriy/volkolak/internal/cmdx"
	"github.com/kkvaleriy/volkolak/internal/fail"
)

type localInput struct {
	ArchivePath string
	DestDir     string
	ArchiveType string
}

type localResult struct {
	Videos  []diskFile
	Removed int
}

// extractLocal распаковывает архив на диск и удаляет всё, что не является видео.
// Повтор начинается с чистого каталога назначения.
func extractLocal(ctx context.Context, in localInput) (localResult, error) {
	if err := ctx.Err(); err != nil {
		return localResult{}, err
	}
	if in.ArchivePath == "" || in.DestDir == "" || in.ArchiveType == "" {
		return localResult{}, fail.Input("нужны путь архива, каталог назначения и тип")
	}
	dest := filepath.Clean(in.DestDir)
	if dest == "/" || dest == "." {
		return localResult{}, fail.Input("небезопасный каталог назначения")
	}

	info, err := os.Stat(in.ArchivePath)
	if err != nil {
		return localResult{}, fail.Input(fmt.Sprintf("архив недоступен: %v", err))
	}
	if !info.Mode().IsRegular() {
		return localResult{}, fail.Input("архив должен быть обычным файлом")
	}
	if isSafePath(dest, in.ArchivePath) || samePath(dest, in.ArchivePath) {
		return localResult{}, fail.Input("архив не должен лежать внутри каталога назначения")
	}

	actlog.Info(ctx, "распаковываю архив", "type", in.ArchiveType, "file", in.ArchivePath, "dest", dest)
	stop := cmdx.Beat(ctx, "extract "+in.ArchiveType)
	defer stop()

	if err := os.RemoveAll(dest); err != nil {
		return localResult{}, fmt.Errorf("очистка каталога: %w", err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return localResult{}, fmt.Errorf("создание каталога: %w", err)
	}

	skipped, err := extractByType(ctx, in.ArchivePath, dest, in.ArchiveType)
	if err != nil {
		_ = os.RemoveAll(dest)
		return localResult{}, err
	}

	videos, pruned, err := pruneNonVideo(dest)
	if err != nil {
		_ = os.RemoveAll(dest)
		return localResult{}, err
	}
	if videos == nil {
		videos = []diskFile{}
	}
	actlog.Info(ctx, "распаковка завершена", "videos", len(videos), "removed", skipped+pruned)
	return localResult{Videos: videos, Removed: skipped + pruned}, nil
}

func extractByType(ctx context.Context, src, dest, archiveType string) (int, error) {
	switch archiveType {
	case "zip":
		return extractZip(ctx, src, dest)
	case "tar":
		return extractTar(ctx, src, dest)
	case "tar.gz":
		return extractTarGzip(ctx, src, dest)
	case "gzip":
		return extractGzipFile(ctx, src, dest)
	case "tar.bz2":
		return extractTarBzip2(ctx, src, dest)
	case "bzip2":
		return extractBzip2File(ctx, src, dest)
	case "7z", "xz":
		return 0, extract7z(ctx, src, dest)
	case "rar":
		return 0, fail.Input("архивы rar не поддерживаются: формат требует unrar с отдельной лицензией")
	default:
		return 0, fail.Input("неподдерживаемый тип архива: " + archiveType)
	}
}

func extractZip(ctx context.Context, src, dest string) (int, error) {
	reader, err := zip.OpenReader(src)
	if err != nil {
		return 0, fmt.Errorf("zip: %w", err)
	}
	defer reader.Close()

	skipped := 0
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return skipped, err
		}
		clean, err := cleanArchivePath(file.Name)
		if err != nil {
			return skipped, err
		}
		if clean == "" || skipNoise(clean) {
			if clean != "" {
				skipped++
			}
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(clean))
		if !isSafePath(dest, target) {
			return skipped, fmt.Errorf("выход за каталог назначения: %q", file.Name)
		}
		if file.FileInfo().IsDir() || strings.HasSuffix(file.Name, "/") {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return skipped, err
			}
			continue
		}
		if !isVideo(clean) {
			skipped++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return skipped, err
		}
		body, err := file.Open()
		if err != nil {
			return skipped, err
		}
		err = writeReader(ctx, target, body, clean)
		body.Close()
		if err != nil {
			return skipped, err
		}
	}
	return skipped, nil
}

func extractTar(ctx context.Context, src, dest string) (int, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return extractTarReader(ctx, tar.NewReader(f), dest)
}

func extractTarGzip(ctx context.Context, src, dest string) (int, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	return extractTarReader(ctx, tar.NewReader(gz), dest)
}

func extractTarBzip2(ctx context.Context, src, dest string) (int, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return extractTarReader(ctx, tar.NewReader(bzip2.NewReader(f)), dest)
}

func extractGzipFile(ctx context.Context, src, dest string) (int, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	return writeSingleCompressed(ctx, dest, singleName(src, gz.Name, ".gz"), gz)
}

func extractBzip2File(ctx context.Context, src, dest string) (int, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return writeSingleCompressed(ctx, dest, singleName(src, "", ".bz2"), bzip2.NewReader(f))
}

func writeSingleCompressed(ctx context.Context, dest, name string, r io.Reader) (int, error) {
	clean, err := cleanArchivePath(name)
	if err != nil {
		return 0, err
	}
	if clean == "" || skipNoise(clean) || !isVideo(clean) {
		return 1, nil
	}
	target := filepath.Join(dest, filepath.FromSlash(clean))
	if !isSafePath(dest, target) {
		return 0, fmt.Errorf("выход за каталог назначения: %q", name)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	if err := writeReader(ctx, target, r, clean); err != nil {
		return 0, err
	}
	return 0, nil
}

func singleName(src, headerName, suffix string) string {
	name := headerName
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(src), suffix)
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." {
		return "content"
	}
	return name
}

func extractTarReader(ctx context.Context, tr *tar.Reader, dest string) (int, error) {
	skipped := 0
	for {
		if err := ctx.Err(); err != nil {
			return skipped, err
		}
		header, err := tr.Next()
		if err == io.EOF {
			return skipped, nil
		}
		if err != nil {
			return skipped, fmt.Errorf("tar: %w", err)
		}
		clean, err := cleanArchivePath(header.Name)
		if err != nil {
			return skipped, err
		}
		if clean == "" || skipNoise(clean) {
			if clean != "" && header.Typeflag != tar.TypeDir {
				skipped++
			}
			if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
				if err := discard(tr, header.Size); err != nil {
					return skipped, err
				}
			}
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(clean))
		if !isSafePath(dest, target) {
			return skipped, fmt.Errorf("выход за каталог назначения: %q", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return skipped, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if !isVideo(clean) {
				skipped++
				if err := discard(tr, header.Size); err != nil {
					return skipped, err
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return skipped, err
			}
			if err := writeN(ctx, target, tr, header.Size, clean); err != nil {
				return skipped, err
			}
		default:
			// Ссылки и спецфайлы не материализуем: ими можно выйти из каталога.
			skipped++
		}
	}
}

func extract7z(ctx context.Context, src, dest string) error {
	bin := os.Getenv("SEVEN_ZIP_BIN")
	if bin == "" {
		bin = "7z"
	}
	if err := rejectUnsafe7zPaths(ctx, bin, src); err != nil {
		return err
	}
	_, err := cmdx.Exec(ctx, bin, "x", "-y", "-bd", "-o"+dest, src)
	if err != nil {
		return fmt.Errorf("7z: %w", err)
	}
	return nil
}

// rejectUnsafe7zPaths не даёт 7z записать файл вне каталога назначения.
// Первая строка Path в листинге — сам архив, её пропускаем.
func rejectUnsafe7zPaths(ctx context.Context, bin, src string) error {
	listing, err := cmdx.Exec(ctx, bin, "l", "-slt", src)
	if err != nil {
		return fmt.Errorf("7z: %w", err)
	}
	first := true
	for _, line := range strings.Split(listing, "\n") {
		line = strings.TrimSpace(line)
		const prefix = "Path = "
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		name := strings.TrimPrefix(line, prefix)
		if first {
			first = false
			continue
		}
		if _, err := cleanArchivePath(name); err != nil {
			return err
		}
	}
	return nil
}

func writeReader(ctx context.Context, target string, r io.Reader, label string) error {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, &ctxReader{ctx: ctx, r: r})
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("%s: %w", label, copyErr)
	}
	return closeErr
}

func writeN(ctx context.Context, target string, r io.Reader, n int64, label string) error {
	if n < 0 {
		return fmt.Errorf("%s: отрицательный размер", label)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.CopyN(out, &ctxReader{ctx: ctx, r: r}, n)
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("%s: %w", label, copyErr)
	}
	return closeErr
}

func discard(r io.Reader, n int64) error {
	if n <= 0 {
		return nil
	}
	_, err := io.CopyN(io.Discard, r, n)
	return err
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && aa == bb
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
