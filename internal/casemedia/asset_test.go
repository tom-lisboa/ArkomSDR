package casemedia

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestVideoServingChecksContentAndSupportsPlaybackRange(t *testing.T) {
	data := append([]byte{0, 0, 0, 24}, []byte("ftypisom")...)
	data = append(data, bytes.Repeat([]byte{0}, 100)...)
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	dir := t.TempDir()
	name := filepath.Join(dir, hash+".mp4")
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	a := Asset{Directory: dir, SHA256: hash, PublicURL: "https://media.example"}
	if !a.Ready() {
		t.Fatal("valid asset was not ready")
	}
	for _, method := range []string{"GET", "HEAD"} {
		r := httptest.NewRequest(method, Prefix+hash+".mp4", nil)
		r.Header.Set("Range", "bytes=4-7")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 206 || w.Header().Get("Content-Type") != "video/mp4" || (method == "GET" && w.Body.String() != "ftyp") || (method == "HEAD" && w.Body.Len() != 0) {
			t.Fatal(method, w.Code, w.Header(), w.Body.String())
		}
	}
	for _, path := range []string{"../.env", "bad.mp4", hash + ".ogg"} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", Prefix+path, nil))
		if w.Code != 404 {
			t.Fatal("invalid path served", path, w.Code)
		}
	}
	data[len(data)-1] = 1
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	if a.Ready() {
		t.Fatal("mutated video accepted under its original hash")
	}
}
