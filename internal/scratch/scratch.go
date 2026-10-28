package scratch

import "os"

// Dir создаёт временный каталог. Его нужно удалить, когда утилита закончила работу:
// постоянное место файлов — объектное хранилище, не диск.
func Dir() (string, func(), error) {
	dir, err := os.MkdirTemp("", "volkolak-")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}
