// Package casemedia serves the approved, immutable MP4 case video from our own
// storage. Neither a lead nor a model can choose the outgoing file.
package casemedia

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"arkom/bia/internal/remindermedia"
)

const Prefix = "/media/cases/"
const MaxSize = 16 * 1024 * 1024

type Asset struct{ Directory, SHA256, PublicURL string }

func (a Asset) URL() string {
	return strings.TrimRight(a.PublicURL, "/") + Prefix + a.SHA256 + ".mp4"
}

func (a Asset) Ready() bool {
	f, _, err := a.open(a.SHA256)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func (a Asset) open(hash string) (*os.File, os.FileInfo, error) {
	if a.Directory == "" || !remindermedia.ValidHash(hash) || hash != a.SHA256 {
		return nil, nil, errors.New("intro_video_not_configured")
	}
	name := filepath.Join(a.Directory, hash+".mp4")
	st, err := os.Lstat(name)
	if err != nil || !st.Mode().IsRegular() || st.Size() < 32 || st.Size() > MaxSize {
		return nil, nil, errors.New("intro_video_invalid_file")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, nil, err
	}
	bad := func() (*os.File, os.FileInfo, error) {
		f.Close()
		return nil, nil, errors.New("intro_video_invalid_content")
	}
	header := make([]byte, 12)
	if _, err = io.ReadFull(f, header); err != nil || !bytes.Equal(header[4:8], []byte("ftyp")) {
		return bad()
	}
	if _, err = f.Seek(0, 0); err != nil {
		return bad()
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, MaxSize+1)); err != nil || hex.EncodeToString(h.Sum(nil)) != hash {
		return bad()
	}
	if _, err = f.Seek(0, 0); err != nil {
		return bad()
	}
	return f, st, nil
}

func (a Asset) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, Prefix)
	if !strings.HasPrefix(r.URL.Path, Prefix) || !strings.HasSuffix(name, ".mp4") {
		http.NotFound(w, r)
		return
	}
	f, st, err := a.open(strings.TrimSuffix(name, ".mp4"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+a.SHA256+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, st.ModTime(), f)
}
