package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
)

// Fingerprinting the stylesheet and the scripts.
//
// The pages are served straight out of the binary, so an upgrade changes
// /static/app.css without changing its address. A browser that still holds the
// previous one keeps it — which is how an upgraded site can render with the
// stylesheet and the scripts of the version before it, looking broken in ways
// that are nowhere in the source. The @ of the Mattermost field landing above
// its box rather than inside it was exactly that.
//
// So every asset is addressed by its content: /static/app.css?v=<hash>. A new
// build is a new address, which no cache can answer from, and the old address
// is never asked for again.

// assetVersions maps a file in static/ to the first bytes of the hash of its
// contents. Built once at startup: the files are embedded, so they cannot
// change under us.
func assetVersions(dir fs.FS) (map[string]string, error) {
	out := map[string]string{}
	err := fs.WalkDir(dir, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(dir, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		// Eight hex characters are plenty to tell two builds apart, and keep
		// the address readable in a page's source.
		out[path] = hex.EncodeToString(sum[:])[:8]
		return nil
	})
	return out, err
}

// asset is the template's way of naming a static file: {{asset "app.css"}}
// gives the address to link, hash and all. An unknown name is returned
// unchanged rather than swallowed, so a typo shows up as a 404 in the log
// instead of a page that quietly lost its stylesheet.
func (s *Server) asset(name string) string {
	if v, ok := s.assets[name]; ok {
		return "/static/" + name + "?v=" + v
	}
	return "/static/" + name
}

// cacheStatic sets how long a static file may be kept.
//
// An address that carries a version can be kept for a year: the contents
// behind it never change, because changing them changes the address. Anything
// asked for without one might be the previous build's address, or a link
// somebody typed, so it gets a short life instead of an hour's — long enough
// to be worth a cache, short enough that a stale copy heals itself.
func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=60")
		}
		next.ServeHTTP(w, r)
	})
}
