package unpack

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

func TestCleanArchivePath(t *testing.T) {
	got, err := cleanArchivePath(`dir\clip.mp4`)
	require.NoError(t, err)
	require.Equal(t, "dir/clip.mp4", got)

	_, err = cleanArchivePath("../etc/passwd")
	require.Error(t, err)
	_, err = cleanArchivePath("foo/../../etc/passwd")
	require.Error(t, err)
	_, err = cleanArchivePath("/etc/passwd")
	require.Error(t, err)

	got, err = cleanArchivePath(".")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestDetectArchiveType(t *testing.T) {
	dir := t.TempDir()

	zipPath := filepath.Join(dir, "a.zip")
	writeZip(t, zipPath, map[string]string{"clip.mp4": "video", "note.txt": "hi"})

	tarGz := filepath.Join(dir, "b.tgz")
	writeTarGz(t, tarGz, map[string]string{"nested/movie.mkv": "video", "readme.txt": "x"})

	plainGz := filepath.Join(dir, "c.gz")
	writeGzip(t, plainGz, "only.mp4", []byte("video"))

	misnamed := filepath.Join(dir, "looks.zip")
	writeGzip(t, misnamed, "only.mp4", []byte("video"))

	ustar := filepath.Join(dir, "old.bin")
	buf := make([]byte, 512)
	copy(buf[257:], []byte("ustar"))
	require.NoError(t, os.WriteFile(ustar, buf, 0o644))

	seven := filepath.Join(dir, "x.7z")
	require.NoError(t, os.WriteFile(seven, []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C, 0, 0}, 0o644))

	rar := filepath.Join(dir, "x.rar")
	require.NoError(t, os.WriteFile(rar, []byte{'R', 'a', 'r', '!', 0x1A, 0x07}, 0o644))

	legacyTar := filepath.Join(dir, "legacy.tar")
	require.NoError(t, os.WriteFile(legacyTar, []byte("not a ustar header"), 0o644))

	unknown := filepath.Join(dir, "nope.bin")
	require.NoError(t, os.WriteFile(unknown, []byte("hello"), 0o644))

	tests := []struct {
		path string
		want string
	}{
		{zipPath, "zip"},
		{tarGz, "tar.gz"},
		{plainGz, "gzip"},
		{misnamed, "gzip"},
		{ustar, "tar"},
		{seven, "7z"},
		{rar, "rar"},
		{legacyTar, "tar"},
	}
	for _, tt := range tests {
		t.Run(filepath.Base(tt.path), func(t *testing.T) {
			got, err := detectFile(context.Background(), tt.path)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}

	_, err := detectFile(context.Background(), unknown)
	require.Error(t, err)
	var app *temporal.ApplicationError
	require.ErrorAs(t, err, &app)
	require.True(t, app.NonRetryable())
}

func TestExtractVideosKeepsOnlyVideos(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "pack.tar.gz")
	writeTarGz(t, archive, map[string]string{
		"nested/clip.MKV": "video-a",
		"movie.mp4":       "video-b",
		"notes.txt":       "drop-me",
		"__MACOSX/._x":    "junk",
	})
	dest := filepath.Join(dir, "out")

	kind, err := detectFile(context.Background(), archive)
	require.NoError(t, err)

	got, err := extractLocal(context.Background(), localInput{
		ArchivePath: archive,
		DestDir:     dest,
		ArchiveType: kind,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"movie.mp4", "nested/clip.MKV"}, rels(got.Videos))
	require.GreaterOrEqual(t, got.Removed, 2)
	_, err = os.Stat(filepath.Join(dest, "notes.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	body, err := os.ReadFile(filepath.Join(dest, "nested", "clip.MKV"))
	require.NoError(t, err)
	require.Equal(t, "video-a", string(body))
}

func TestExtractZipRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "bad.zip")
	writeZip(t, archive, map[string]string{"../pwned.txt": "nope", "ok.mp4": "video"})
	dest := filepath.Join(dir, "out")

	_, err := extractLocal(context.Background(), localInput{
		ArchivePath: archive,
		DestDir:     dest,
		ArchiveType: "zip",
	})
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(dir, "pwned.txt"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestExtractRarIsNotRetryable(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.rar")
	require.NoError(t, os.WriteFile(archive, []byte("rar"), 0o644))

	_, err := extractLocal(context.Background(), localInput{
		ArchivePath: archive,
		DestDir:     filepath.Join(dir, "out"),
		ArchiveType: "rar",
	})
	var app *temporal.ApplicationError
	require.ErrorAs(t, err, &app)
	require.True(t, app.NonRetryable())
}

func rels(videos []diskFile) []string {
	out := make([]string, len(videos))
	for i, video := range videos {
		out[i] = video.RelativePath
	}
	return out
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
}

func writeGzip(t *testing.T, path, name string, content []byte) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	zw := gzip.NewWriter(f)
	zw.Name = name
	_, err = zw.Write(content)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
}

func writeTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}))
		_, err = tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
}
