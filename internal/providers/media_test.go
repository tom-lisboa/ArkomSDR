package providers

import (
	"arkom/bia/internal/conversation"
	"context"
	"encoding/json"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestMediaUsesBoundedDownloadAndSeparateResponse(t *testing.T) {
	for _, kind := range []string{"image", "document"} {
		t.Run(kind, func(t *testing.T) {
			cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "image" {
					w.Header().Set("Content-Type", "image/png")
				} else {
					w.Header().Set("Content-Type", "application/pdf")
				}
				_, _ = w.Write([]byte("synthetic file"))
			}))
			defer cdn.Close()
			ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var p map[string]any
				_ = json.NewDecoder(r.Body).Decode(&p)
				if p["store"] != false || p["previous_response_id"] != nil {
					t.Error("media must not extend conversation chain")
				}
				input := p["input"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
				expected := "input_image"
				if kind == "document" {
					expected = "input_file"
				}
				if input["type"] != expected {
					t.Error(input)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Texto extraído"}]}]}`))
			}))
			defer ai.Close()
			// Production never uses this transport; TLS fixture redirects the logical CDN
			// to a local test server while exercising host validation and SDK serialization.
			transport := rewriteMediaTransport{target: cdn.URL, base: cdn.Client().Transport}
			m := Media{Model: "test", Hosts: map[string]bool{"cdn.example": true}, HTTP: &http.Client{Transport: transport}, AI: openai.NewClient(option.WithAPIKey("test"), option.WithBaseURL(ai.URL+"/"), option.WithMaxRetries(0))}
			text, e := m.Process(context.Background(), conversation.Attachment{Kind: kind, URL: "https://cdn.example/file"})
			if e != nil || text != "Texto extraído" {
				t.Fatal(text, e)
			}
		})
	}
}

type rewriteMediaTransport struct {
	target string
	base   http.RoundTripper
}

func (t rewriteMediaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	u, _ := url.Parse(t.target + r.URL.Path)
	copy.URL = u
	return t.base.RoundTrip(copy)
}
