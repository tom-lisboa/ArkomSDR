package lumi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCredentialsAreBoundToSellerAndMasterKey(t *testing.T) {
	secret := strings.Repeat("x", 32)
	raw, err := Seal(secret, "seller-a", "private-instance-key")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-instance-key") {
		t.Fatal("plaintext credential")
	}
	if out, err := Open(secret, "seller-a", raw); err != nil || out != "private-instance-key" {
		t.Fatal(out, err)
	}
	if _, err := Open(secret, "seller-b", raw); err == nil {
		t.Fatal("credential moved to another seller")
	}
	if _, err := Open(strings.Repeat("y", 32), "seller-a", raw); err == nil {
		t.Fatal("wrong master key")
	}
	raw[len(raw)-1] ^= 1
	if _, err := Open(secret, "seller-a", raw); err == nil {
		t.Fatal("tampered credential accepted")
	}
}
func TestCredentialsNeverFollowRedirectsAndErrorsAreRedacted(t *testing.T) {
	var leaked atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer upstream.Close()
	c, err := New(upstream.URL, "user-secret")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Send(context.Background(), "instance-secret", "+5511999999000", "test")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Status != 307 || leaked.Load() != 0 {
		t.Fatal(err, leaked.Load())
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("secret in error")
	}
}
