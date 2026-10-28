// Package remindermedia serves immutable, pre-recorded reminder audio from our
// own storage. No URL from a lead or an LLM can select the outgoing recording.
package remindermedia

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
)

const MaxSize = 16 * 1024 * 1024
const Prefix = "/media/reminders/"

type Asset struct{ Directory, SHA256, PublicURL string }

func ValidHash(hash string) bool {
	b, err := hex.DecodeString(hash)
	return err == nil && len(b) == 32 && hash == strings.ToLower(hash)
}
func (a Asset) URL() string { return strings.TrimRight(a.PublicURL, "/") + Prefix + a.SHA256 + ".ogg" }
func (a Asset) Ready() bool {
	f, _, err := a.open(a.SHA256)
	if err != nil {
		return false
	}
	f.Close()
	return true
}
func (a Asset) open(hash string) (*os.File, os.FileInfo, error) {
	if !ValidHash(hash) || a.Directory == "" {
		return nil, nil, errors.New("reminder_audio_not_configured")
	}
	path := filepath.Join(a.Directory, hash+".ogg")
	stat, err := os.Lstat(path)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() < 32 || stat.Size() > MaxSize {
		return nil, nil, errors.New("reminder_audio_invalid_file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	bad := func() (*os.File, os.FileInfo, error) {
		f.Close()
		return nil, nil, errors.New("reminder_audio_invalid_content")
	}
	header := make([]byte, min(stat.Size(), 4096))
	if _, err = io.ReadFull(f, header); err != nil {
		return bad()
	}
	if !bytes.HasPrefix(header, []byte("OggS")) || !bytes.Contains(header, []byte("OpusHead")) {
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
	return f, stat, nil
}
func (a Asset) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, Prefix)
	if !strings.HasPrefix(r.URL.Path, Prefix) || !strings.HasSuffix(name, ".ogg") {
		http.NotFound(w, r)
		return
	}
	hash := strings.TrimSuffix(name, ".ogg")
	f, st, err := a.open(hash)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "audio/ogg")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+hash+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, st.ModTime(), f)
}
