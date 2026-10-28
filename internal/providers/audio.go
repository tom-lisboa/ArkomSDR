package providers

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
)

type Audio struct {
	Download *Downloader
	AI       openai.Client
	Model    string
	Hosts    map[string]bool
	HTTP     *http.Client
}

// Resolve and dial public IPs explicitly: approved media hosts cannot redirect
// a download to the internal network, including through DNS rebinding.
func MediaHTTP() *http.Client {
	t := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		for _, v := range ips {
			if v.IP.IsPrivate() || v.IP.IsLoopback() || v.IP.IsLinkLocalUnicast() || !v.IP.IsGlobalUnicast() {
				return nil, errors.New("private_media_address")
			}
		}
		if len(ips) == 0 {
			return nil, errors.New("media_dns_empty")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}}
	return &http.Client{Transport: t, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (a *Audio) Transcribe(ctx context.Context, raw string) (string, error) {
	downloader := a.Download
	if downloader == nil {
		downloader = &Downloader{Hosts: a.Hosts, HTTP: a.HTTP}
	}
	b, ct, e := downloader.Download(ctx, raw)
	if e != nil {
		return "", e
	}
	if !strings.HasPrefix(ct, "audio/") && ct != "application/ogg" {
		return "", &Failure{Code: "audio_invalid_type"}
	}
	name := "audio.ogg"
	if strings.Contains(ct, "webm") {
		name = "audio.webm"
	}
	if strings.Contains(ct, "flac") {
		name = "audio.flac"
	}
	if strings.Contains(ct, "mpeg") {
		name = "audio.mp3"
	}
	if strings.Contains(ct, "wav") {
		name = "audio.wav"
	}
	if strings.Contains(ct, "mp4") {
		name = "audio.m4a"
	}
	r, e := a.AI.Audio.Transcriptions.New(ctx, openai.AudioTranscriptionNewParams{File: openai.File(bytes.NewReader(b), name, ct), Model: openai.AudioModel(a.Model), Language: openai.String("pt")})
	if e != nil {
		return "", &Failure{Code: "transcription_failed"}
	}
	text := strings.TrimSpace(r.Text)
	if text == "" || len(text) > 16000 || strings.Contains(strings.ToLower(text), "amara.org") {
		return "", &Failure{Code: "transcription_unusable"}
	}
	return text, nil
}
