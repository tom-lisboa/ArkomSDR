// Package googlecalendar owns the Google OAuth connection. It does not replace
// the SDR calendar or create external events implicitly.
package googlecalendar

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const CallbackPath = "/v1/management/integrations/google/callback"
const DefaultRedirectURL = "https://sdk.arkom.ai" + CallbackPath
const CalendarListScope = "https://www.googleapis.com/auth/calendar.calendarlist.readonly"

type Config struct {
	Enabled                                       bool
	ClientID, ClientSecret, RedirectURL, TokenKey string
}

func (c Config) Validate() error {
	u, err := url.Parse(c.RedirectURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != CallbackPath || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || net.ParseIP(u.Hostname()) != nil {
		return errors.New("invalid BIA_GOOGLE_REDIRECT_URL")
	}
	if c.Enabled && (strings.TrimSpace(c.ClientID) == "" || strings.TrimSpace(c.ClientSecret) == "" || len(c.TokenKey) < 32) {
		return errors.New("Google OAuth requires BIA_GOOGLE_CLIENT_ID, BIA_GOOGLE_CLIENT_SECRET and BIA_GOOGLE_TOKEN_KEY (32+ characters)")
	}
	return nil
}

type Client struct {
	Config Config
	OAuth  oauth2.Config
	HTTP   *http.Client
}

func New(c Config) (*Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, nil
	}
	return &Client{Config: c, OAuth: oauth2.Config{
		ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL,
		Scopes:   []string{CalendarListScope, "openid", "email"},
		Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", AuthStyle: oauth2.AuthStyleInParams},
	}, HTTP: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, c.HTTP)
}

func (c *Client) AuthorizationURL(state, verifier string) string {
	return c.OAuth.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent select_account"), oauth2.S256ChallengeOption(verifier))
}

type Identity struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

// Identity comes from Google's fixed HTTPS authority, never from browser data.
func (c *Client) Identity(ctx context.Context, token *oauth2.Token) (Identity, error) {
	var identity Identity
	req, err := http.NewRequestWithContext(ctx, "GET", "https://openidconnect.googleapis.com/v1/userinfo", nil)
	if err != nil {
		return identity, err
	}
	token.SetAuthHeader(req)
	r, err := c.HTTP.Do(req)
	if err != nil {
		return identity, errors.New("google_identity_unavailable")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 || json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&identity) != nil || identity.Subject == "" || len(identity.Subject) > 255 || identity.Email == "" || len(identity.Email) > 320 || !identity.EmailVerified {
		return Identity{}, errors.New("google_identity_unavailable")
	}
	return identity, nil
}

func (c *Client) Seal(subject string, value []byte) ([]byte, error) {
	a, err := c.vault()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, value, []byte(c.Config.ClientID+"\x00"+subject)), nil
}

func (c *Client) Open(subject string, raw []byte) ([]byte, error) {
	a, err := c.vault()
	if err != nil {
		return nil, err
	}
	if len(raw) < a.NonceSize() {
		return nil, errors.New("invalid Google credential")
	}
	return a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], []byte(c.Config.ClientID+"\x00"+subject))
}

func (c *Client) vault() (cipher.AEAD, error) {
	if len(c.Config.TokenKey) < 32 {
		return nil, errors.New("Google token encryption key required")
	}
	key := sha256.Sum256([]byte("arkom/google/oauth/v1\x00" + c.Config.TokenKey))
	b, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

type Calendar struct {
	ID         string `json:"id"`
	Summary    string `json:"summary"`
	TimeZone   string `json:"timeZone"`
	AccessRole string `json:"accessRole"`
	Primary    bool   `json:"primary"`
}

// Calendars is read-only and exposes only the fields the staff frontend needs.
// Tokens and raw Google errors must never be returned to the browser.
func (c *Client) Calendars(ctx context.Context, token *oauth2.Token, page string) ([]Calendar, string, error) {
	u := "https://www.googleapis.com/calendar/v3/users/me/calendarList?maxResults=100"
	if page != "" {
		u += "&pageToken=" + url.QueryEscape(page)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, "", err
	}
	token.SetAuthHeader(req)
	r, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", errors.New("google_calendar_unavailable")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, "", errors.New("google_calendar_unavailable")
	}
	var out struct {
		Items []Calendar `json:"items"`
		Next  string     `json:"nextPageToken"`
	}
	if err = json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&out); err != nil {
		return nil, "", errors.New("google_calendar_unavailable")
	}
	if out.Items == nil {
		out.Items = []Calendar{}
	}
	return out.Items, out.Next, nil
}
