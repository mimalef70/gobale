// Package web validates and serves the admin UI embedded into a GoBale binary.
// Node is a build dependency only; no files are downloaded or executed at runtime.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/gofiber/fiber/v3"
)

//go:embed all:dist
var embedded embed.FS

const baseMarker = "__GOBALE_UI_BASE__"
const buildHelp = "build the administrative UI with npm ci && npm run build in ui/ before building GoBale, or disable APP_UI_ENABLED"

type Manifest struct {
	SchemaVersion int               `json:"schema_version"`
	Version       string            `json:"version"`
	OpenAPISHA256 string            `json:"openapi_sha256"`
	Files         map[string]string `json:"files"`
}

// Bundle is immutable after validation. Only manifest-listed assets are served.
type Bundle struct {
	manifest Manifest
	files    map[string][]byte
}

func Validate(version, openAPISHA256 string) (*Bundle, error) {
	root, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, fmt.Errorf("administrative UI assets unavailable: %s", buildHelp)
	}
	return ValidateFS(root, version, openAPISHA256)
}

// ValidateFS also allows isolated installation tests without an npm build. The
// filesystem root must contain the manifest and assets, not a parent dist folder.
func ValidateFS(root fs.FS, version, openAPISHA256 string) (*Bundle, error) {
	bad := func(reason string) (*Bundle, error) {
		return nil, fmt.Errorf("administrative UI assets invalid (%s): %s", reason, buildHelp)
	}
	data, err := fs.ReadFile(root, "build-manifest.json")
	if err != nil {
		return bad("production manifest missing")
	}
	if len(data) > 1<<20 {
		return bad("manifest exceeds limit")
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&manifest); err != nil {
		return bad("malformed manifest")
	}
	if !errors.Is(decoder.Decode(new(any)), io.EOF) {
		return bad("multiple manifest values")
	}
	if manifest.SchemaVersion != 1 || version == "" || manifest.Version != version {
		return bad("asset and server versions differ")
	}
	if !validDigest(manifest.OpenAPISHA256) || (openAPISHA256 != "" && manifest.OpenAPISHA256 != openAPISHA256) {
		return bad("asset and API contracts differ")
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > 4096 || manifest.Files["index.html"] == "" {
		return bad("incomplete asset inventory")
	}
	bundle := &Bundle{manifest: manifest, files: make(map[string][]byte, len(manifest.Files))}
	var total int64
	for name, digest := range manifest.Files {
		if !validAssetName(name) || !validDigest(digest) {
			return bad("unsafe asset inventory")
		}
		info, err := fs.Stat(root, name)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > 32<<20 {
			return bad("asset missing or oversized")
		}
		total += info.Size()
		if total > 128<<20 {
			return bad("bundle exceeds limit")
		}
		asset, err := fs.ReadFile(root, name)
		if err != nil {
			return bad("asset unavailable")
		}
		sum := sha256.Sum256(asset)
		if hex.EncodeToString(sum[:]) != digest {
			return bad("asset checksum mismatch")
		}
		bundle.files[name] = asset
	}
	if bytes.Count(bundle.files["index.html"], []byte(baseMarker)) != 1 || !bytes.Contains(bundle.files["index.html"], []byte(`<base href="`+baseMarker+`/"`)) {
		return bad("index base path marker missing")
	}
	err = fs.WalkDir(root, ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink in bundle")
		}
		if name == "build-manifest.json" || name == ".placeholder" {
			return nil
		}
		if _, ok := manifest.Files[name]; !ok {
			return fmt.Errorf("unlisted file")
		}
		return nil
	})
	if err != nil {
		return bad("unlisted files in bundle")
	}
	return bundle, nil
}

func validDigest(s string) bool {
	if len(s) != 64 || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func validAssetName(name string) bool {
	if name == "index.html" {
		return true
	}
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\%?#\x00") || path.Clean(name) != name {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || strings.HasPrefix(component, ".") {
			return false
		}
		for _, ch := range component {
			if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' && ch != '.' {
				return false
			}
		}
	}
	return contentType(name) != ""
}
func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		if name == "index.html" {
			return "text/html; charset=utf-8"
		}
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".ttf":
		return "font/ttf"
	case ".txt":
		return "text/plain; charset=utf-8"
	}
	return ""
}

// Handler serves only /ui, /ui/, index.html and the validated finite inventory.
// The UI uses hash routing; arbitrary unknown paths never fall back to HTML.
func (b *Bundle) Handler(basePath string) fiber.Handler {
	prefix := strings.TrimRight(basePath, "/") + "/ui"
	return func(c fiber.Ctx) error {
		c.Set("X-Content-Type-Options", "nosniff")
		raw := string(c.Request().URI().PathOriginal())
		if strings.ContainsAny(raw, "%\\") || path.Clean(raw) != strings.TrimRight(raw, "/") {
			return c.SendStatus(404)
		}
		name := ""
		if raw == prefix || raw == prefix+"/" {
			name = "index.html"
		} else if strings.HasPrefix(raw, prefix+"/") {
			name = strings.TrimPrefix(raw, prefix+"/")
		}
		asset, ok := b.files[name]
		if !ok {
			return c.SendStatus(404)
		}
		c.Set("Content-Type", contentType(name))
		if name == "index.html" {
			c.Set("Cache-Control", "no-store")
			return c.Send(bytes.Replace(asset, []byte(baseMarker), []byte(html.EscapeString(prefix)), 1))
		}
		// Vite's content-hashed files live under assets/. Any unhashed root icon
		// gets revalidated instead of an immutable year-long cache entry.
		basename := strings.TrimSuffix(path.Base(name), path.Ext(name))
		// Vite's eight-character hash alphabet itself includes '-' and '_'.
		// Splitting at the final hyphen would misclassify valid hashed fonts.
		hashed := len(basename) > 9 && basename[len(basename)-9] == '-' && !strings.Contains(basename[len(basename)-8:], ".")
		if strings.HasPrefix(name, "assets/") && hashed {
			c.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			c.Set("Cache-Control", "no-cache")
		}
		c.Set("ETag", `"`+b.manifest.Files[name]+`"`)
		if c.Get("If-None-Match") == `"`+b.manifest.Files[name]+`"` {
			return c.SendStatus(304)
		}
		return c.Send(asset)
	}
}
