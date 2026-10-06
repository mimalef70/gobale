// Package mediafile resolves private stored assets without binding new uploads
// to the machine-specific absolute media root.
package mediafile

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/mimalef70/gobale/src/domains"
)

// Resolve accepts new relative references and legacy absolute references inside
// the configured root. Both lexical traversal and symlink escapes are rejected.
func Resolve(root, stored string) (string, error) {
	invalid := func() (string, error) {
		return "", domains.E("MEDIA_INVALID", "invalid stored media path", 500)
	}
	if stored == "" || strings.IndexByte(stored, 0) >= 0 {
		return invalid()
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return invalid()
	}
	path := filepath.FromSlash(stored)
	if !filepath.IsAbs(path) {
		path = filepath.Join(absRoot, path)
	}
	path = filepath.Clean(path)
	if !inside(absRoot, path) {
		return invalid()
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", domains.E("MEDIA_NOT_FOUND", "media directory unavailable", 404)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", domains.E("MEDIA_NOT_FOUND", "media file unavailable", 404)
	}
	if !inside(realRoot, realPath) {
		return invalid()
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", domains.E("MEDIA_NOT_FOUND", "media file unavailable", 404)
	}
	return realPath, nil
}

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
