// Package fixtures holds the small scenario, calibration file, subject, change folders and scripted human the dry
// run and the tests of the context bench use. Nothing here is a real product.
package fixtures

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed scenarios calibration subjects changes human-script.txt fake-build.sh
var files embed.FS

// Extract writes the fixtures under dir (scripts executable).
func Extract(dir string) error {
	return fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		return os.WriteFile(dst, b, mode)
	})
}
