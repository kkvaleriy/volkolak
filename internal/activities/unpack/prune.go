package unpack

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// pruneNonVideo удаляет всё, что не является видео, и пустые каталоги.
func pruneNonVideo(root string) ([]diskFile, int, error) {
	var remove []string
	var videos []diskFile

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if name == "__MACOSX" {
				remove = append(remove, path)
				return filepath.SkipDir
			}
			return nil
		}
		if name == ".DS_Store" || strings.HasPrefix(name, "._") || !isVideo(name) {
			remove = append(remove, path)
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		videos = append(videos, diskFile{
			Path:         path,
			RelativePath: rel,
			Filename:     name,
		})
		return nil
	})
	if err != nil {
		return nil, 0, err
	}

	removed := 0
	for _, path := range remove {
		if err := os.RemoveAll(path); err != nil {
			return nil, 0, err
		}
		removed++
	}
	if err := removeEmptyDirs(root); err != nil {
		return nil, 0, err
	}

	sort.Slice(videos, func(i, j int) bool {
		return videos[i].RelativePath < videos[j].RelativePath
	})
	return videos, removed, nil
}

func removeEmptyDirs(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if len(entries) == 0 {
			if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
