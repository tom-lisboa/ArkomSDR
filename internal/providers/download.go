package providers

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// Downloader authenticates only canonical media resources belonging to our
// Twilio account. Redirects are fetched without credentials through public-IP
// validation. Signed CDN URLs and binary content are never persisted or logged.
type Downloader struct {
	Hosts              map[string]bool
	HTTP               *http.Client
	Account, AuthToken string
}

func validMediaSID(id string) bool {
	if len(id) != 34 || !strings.HasPrefix(id, "ME") {
		return false
	}
	_, e := hex.DecodeString(id[2:])
	return e == nil
}
func (d *Downloader) twilioURL(u *url.URL) bool {
	p := strings.Split(u.Path, "/")
	return d.Account != "" && d.AuthToken != "" && u.Host == "api.twilio.com" && u.RawQuery == "" && u.RawPath == "" && len(p) == 8 && p[1] == "2010-04-01" && p[2] == "Accounts" && p[3] == d.Account && p[4] == "Messages" && ValidMessageSID(p[5]) && p[6] == "Media" && validMediaSID(p[7])
}
func safeMediaURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.User == nil && u.Fragment == "" && (u.Port() == "" || u.Port() == "443")
}
func (d *Downloader) Download(ctx context.Context, raw string) ([]byte, string, error) {
	u, e := url.Parse(raw)
	if e != nil || !safeMediaURL(u) {
		return nil, "", errors.New("untrusted_media_host")
	}
	authenticated := d.twilioURL(u)
	if !authenticated && (!d.Hosts[u.Hostname()] || u.Hostname() == "api.twilio.com") {
		return nil, "", errors.New("untrusted_media_host")
	}
	client := *d.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for hop := 0; hop < 4; hop++ {
		req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if e != nil {
			return nil, "", errors.New("media_invalid_url")
		}
		if authenticated && hop == 0 {
			req.SetBasicAuth(d.Account, d.AuthToken)
		}
		resp, e := client.Do(req)
		if e != nil {
			return nil, "", errors.New("media_download_failed")
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			next, err := resp.Location()
			resp.Body.Close()
			// Only authenticated Twilio downloads may follow signed CDN redirects.
			// MediaHTTP independently rejects private IPs at every connection.
			if !authenticated || err != nil || !safeMediaURL(next) {
				return nil, "", errors.New("media_redirect_rejected")
			}
			u = next
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, "", errors.New("media_download_failed")
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, (20<<20)+1))
		resp.Body.Close()
		if err != nil || len(b) == 0 || len(b) > 20<<20 {
			return nil, "", errors.New("media_invalid_size")
		}
		ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		return b, ct, nil
	}
	return nil, "", errors.New("media_redirect_limit")
}
