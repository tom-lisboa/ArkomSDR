package remindermedia

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestImmutableAudioServing(t *testing.T) {
	data := append([]byte("OggS-test-fixture-OpusHead"), bytes.Repeat([]byte{0}, 100)...)
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	dir := t.TempDir()
	path := filepath.Join(dir, hash+".ogg")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	a := Asset{Directory: dir, SHA256: hash, PublicURL: "https://media.example"}
	if !a.Ready() {
		t.Fatal("asset not ready")
	}
	for _, method := range []string{"GET", "HEAD"} {
		r := httptest.NewRequest(method, Prefix+hash+".ogg", nil)
		r.Header.Set("Range", "bytes=0-3")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 206 || w.Header().Get("Content-Type") != "audio/ogg" || (method == "GET" && w.Body.String() != "OggS") || (method == "HEAD" && w.Body.Len() != 0) {
			t.Fatal(w.Code, w.Header(), w.Body.String())
		}
	}
	for _, bad := range []string{"../../.env", "not-a-hash.ogg", hash + ".mp3"} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", Prefix+bad, nil))
		if w.Code != 404 {
			t.Fatal(bad, w.Code)
		}
	}
	data[10] = 'x'
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if a.Ready() {
		t.Fatal("mutated file passed checksum")
	}
}
