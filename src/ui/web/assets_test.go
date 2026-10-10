package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

const testVersion = "0.2.0-alpha.1"

var testAPIDigest = strings.Repeat("a", 64)

func testBundleFS(t *testing.T) (fstest.MapFS, Manifest) {
	t.Helper()
	files := fstest.MapFS{
		"index.html":                 {Data: []byte(`<!doctype html><html><head><base href="__GOOMNI_UI_BASE__/"><script type="module" src="assets/index-12345678.js"></script></head><body>Admin</body></html>`)},
		"assets/index-12345678.js":   {Data: []byte(`document.title = "GoOmni";`)},
		"assets/font-12345678.woff2": {Data: []byte("synthetic-font")},
		"assets/font-CkhJZR-_.woff2": {Data: []byte("synthetic-font-with-url-safe-hash")},
		"icon.svg":                   {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
		"THIRD_PARTY_NOTICES.txt":    {Data: []byte("Synthetic test notice")},
		".placeholder":               {Data: []byte("build marker")},
	}
	manifest := Manifest{SchemaVersion: 1, Version: testVersion, OpenAPISHA256: testAPIDigest, Files: map[string]string{}}
	for name, file := range files {
		if name == ".placeholder" {
			continue
		}
		sum := sha256.Sum256(file.Data)
		manifest.Files[name] = hex.EncodeToString(sum[:])
	}
	setManifest(t, files, manifest)
	return files, manifest
}
func setManifest(t *testing.T, files fstest.MapFS, manifest Manifest) {
	t.Helper()
	b, err := json.Marshal(manifest)
	require.NoError(t, err)
	files["build-manifest.json"] = &fstest.MapFile{Data: b}
}
func TestValidateAdminBundleIntegrity(t *testing.T) {
	files, _ := testBundleFS(t)
	bundle, err := ValidateFS(files, testVersion, testAPIDigest)
	require.NoError(t, err)
	require.Len(t, bundle.files, 6)
	for _, tc := range []struct {
		name            string
		mutate          func(fstest.MapFS, *Manifest)
		version, digest string
	}{
		{"version", nil, "another-version", testAPIDigest},
		{"contract", nil, testVersion, strings.Repeat("b", 64)},
		{"missing manifest", func(f fstest.MapFS, m *Manifest) { delete(f, "build-manifest.json") }, testVersion, testAPIDigest},
		{"missing asset", func(f fstest.MapFS, m *Manifest) { delete(f, "assets/index-12345678.js") }, testVersion, testAPIDigest},
		{"changed asset", func(f fstest.MapFS, m *Manifest) { f["assets/index-12345678.js"].Data = []byte("tampered") }, testVersion, testAPIDigest},
		{"unlisted asset", func(f fstest.MapFS, m *Manifest) { f["extra.js"] = &fstest.MapFile{Data: []byte("extra")} }, testVersion, testAPIDigest},
		{"hidden secret", func(f fstest.MapFS, m *Manifest) { f[".env"] = &fstest.MapFile{Data: []byte("private")} }, testVersion, testAPIDigest},
		{"schema", func(f fstest.MapFS, m *Manifest) { m.SchemaVersion = 2; setManifest(t, f, *m) }, testVersion, testAPIDigest},
		{"missing index", func(f fstest.MapFS, m *Manifest) { delete(m.Files, "index.html"); setManifest(t, f, *m) }, testVersion, testAPIDigest},
		{"malformed digest", func(f fstest.MapFS, m *Manifest) { m.OpenAPISHA256 = "xyz"; setManifest(t, f, *m) }, testVersion, ""},
		{"trailing junk", func(f fstest.MapFS, m *Manifest) {
			f["build-manifest.json"].Data = append(f["build-manifest.json"].Data, []byte("broken")...)
		}, testVersion, testAPIDigest},
		{"second object", func(f fstest.MapFS, m *Manifest) {
			f["build-manifest.json"].Data = append(f["build-manifest.json"].Data, []byte("{}")...)
		}, testVersion, testAPIDigest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, m := testBundleFS(t)
			if tc.mutate != nil {
				tc.mutate(files, &m)
			}
			_, err := ValidateFS(files, tc.version, tc.digest)
			require.Error(t, err)
			require.Contains(t, err.Error(), "npm ci")
		})
	}
}

func TestValidateAdminBundleRejectsUnsafePathsAndIndex(t *testing.T) {
	for _, name := range []string{"../secret.js", "/secret.js", "assets/../secret.js", "assets/.env", ".private.js", "assets/script%20.js", "assets\\script.js", "assets/script.js?x=1", "assets/script.js#hash", "assets/source.js.map", "assets/shell.html"} {
		t.Run(name, func(t *testing.T) {
			files, m := testBundleFS(t)
			m.Files[name] = strings.Repeat("a", 64)
			setManifest(t, files, m)
			_, err := ValidateFS(files, testVersion, testAPIDigest)
			require.Error(t, err)
		})
	}
	for _, index := range []string{`<html>missing base</html>`, `<base href="__GOOMNI_UI_BASE__/"><div>__GOOMNI_UI_BASE__</div>`} {
		files, m := testBundleFS(t)
		files["index.html"].Data = []byte(index)
		sum := sha256.Sum256([]byte(index))
		m.Files["index.html"] = hex.EncodeToString(sum[:])
		setManifest(t, files, m)
		_, err := ValidateFS(files, testVersion, testAPIDigest)
		require.Error(t, err)
	}
}

func TestAdminBundleServingBasePathAndFiniteAssets(t *testing.T) {
	files, _ := testBundleFS(t)
	bundle, err := ValidateFS(files, testVersion, testAPIDigest)
	require.NoError(t, err)
	app := fiber.New()
	app.Get("/*", bundle.Handler("/gateway"))
	for _, target := range []string{"/gateway/ui", "/gateway/ui/", "/gateway/ui/index.html"} {
		res, err := app.Test(httptest.NewRequest("GET", target, nil))
		require.NoError(t, err)
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		require.NoError(t, err)
		require.Equal(t, 200, res.StatusCode, target)
		require.Contains(t, string(body), `<base href="/gateway/ui/">`)
		require.NotContains(t, string(body), baseMarker)
		require.Equal(t, "no-store", res.Header.Get("Cache-Control"))
	}
	for _, target := range []string{"/gateway/ui/unknown", "/gateway/ui/build-manifest.json", "/gateway/ui/.placeholder", "/gateway/ui/.env", "/gateway/ui/assets/../icon.svg", "/gateway/ui/%2e%2e/index.html", "/gateway/ui/assets%2findex-12345678.js", "/other/ui/index.html"} {
		res, err := app.Test(httptest.NewRequest("GET", target, nil))
		require.NoError(t, err)
		res.Body.Close()
		require.Equal(t, 404, res.StatusCode, target)
	}
	res, err := app.Test(httptest.NewRequest("GET", "/gateway/ui/assets/index-12345678.js", nil))
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	require.Equal(t, "public, max-age=31536000, immutable", res.Header.Get("Cache-Control"))
	require.Equal(t, "text/javascript; charset=utf-8", res.Header.Get("Content-Type"))
	etag := res.Header.Get("ETag")
	require.NotEmpty(t, etag)
	req := httptest.NewRequest("GET", "/gateway/ui/assets/index-12345678.js", nil)
	req.Header.Set("If-None-Match", etag)
	res, err = app.Test(req)
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, 304, res.StatusCode)
	res, err = app.Test(httptest.NewRequest("GET", "/gateway/ui/THIRD_PARTY_NOTICES.txt", nil))
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	require.Equal(t, "no-cache", res.Header.Get("Cache-Control"))
	res, err = app.Test(httptest.NewRequest("GET", "/gateway/ui/assets/font-CkhJZR-_.woff2", nil))
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, "public, max-age=31536000, immutable", res.Header.Get("Cache-Control"))
}
