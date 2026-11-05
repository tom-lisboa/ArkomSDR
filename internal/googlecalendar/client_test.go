package googlecalendar

import (
	"net/url"
	"strings"
	"testing"
)

func TestConfigAndPKCE(t *testing.T) {
	c := Config{Enabled: true, ClientID: "client", ClientSecret: "secret", RedirectURL: DefaultRedirectURL, TokenKey: strings.Repeat("k", 64)}
	client, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(client.AuthorizationURL("state", "test-verifier"))
	if q := u.Query(); q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("code_verifier") != "" || q.Get("redirect_uri") != DefaultRedirectURL || q.Get("access_type") != "offline" || q.Get("scope") != CalendarListScope || q.Get("state") != "state" {
		t.Fatal("invalid authorization URL")
	}
	for _, bad := range []string{"http://sdk.arkom.ai" + CallbackPath, "https://24.199.100.186" + CallbackPath, DefaultRedirectURL + "?next=evil", DefaultRedirectURL + "#fragment", DefaultRedirectURL + "/", "https://user@host" + CallbackPath} {
		c.RedirectURL = bad
		if _, err := New(c); err == nil {
			t.Fatal("accepted invalid redirect", bad)
		}
	}
	c.RedirectURL = DefaultRedirectURL
	c.TokenKey = ""
	if _, err := New(c); err == nil {
		t.Fatal("enabled without encryption key")
	}
	c.Enabled = false
	if client, err := New(c); err != nil || client != nil {
		t.Fatal("disabled integration requires credentials", err)
	}
}

func TestCredentialCannotMoveBetweenUsersProvidersOrPurposes(t *testing.T) {
	c, err := New(Config{Enabled: true, ClientID: "client", ClientSecret: "secret", RedirectURL: DefaultRedirectURL, TokenKey: strings.Repeat("k", 64)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.Seal("token:alice", []byte("refresh-secret"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := c.Open("token:alice", raw)
	if err != nil || string(opened) != "refresh-secret" {
		t.Fatal("failed roundtrip", err)
	}
	for _, subject := range []string{"token:bob", "state:alice"} {
		if _, err := c.Open(subject, raw); err == nil {
			t.Fatal("credential moved", subject)
		}
	}
	c.Config.ClientID = "different-client"
	if _, err := c.Open("token:alice", raw); err == nil {
		t.Fatal("credential moved between OAuth apps")
	}
	c.Config.ClientID = "client"
	raw[len(raw)-1] ^= 1
	if _, err := c.Open("token:alice", raw); err == nil {
		t.Fatal("accepted tampered ciphertext")
	}
}
