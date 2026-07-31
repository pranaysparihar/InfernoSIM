package generator

import "os"

func writeTestFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
