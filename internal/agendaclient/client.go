// Package agendaclient publishes requests; only the scheduling worker answers.
package agendaclient

import (
	"arkom/bia/internal/domain"
	"arkom/bia/internal/store"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type Client struct{ DB *store.DB }

func (c Client) Lookup(ctx context.Context, request string) (domain.MeetingContext, error) {
	var out domain.MeetingContext
	id := make([]byte, 16)
	if _, e := rand.Read(id); e != nil {
		return out, e
	}
	raw, e := c.request(ctx, hex.EncodeToString(id), request, "lookup", "")
	if e == nil {
		e = json.Unmarshal(raw, &out)
	}
	return out, e
}
func (c Client) Note(ctx context.Context, request, callID, body string) (string, error) {
	key := sha256.Sum256([]byte(request + "/" + callID))
	raw, e := c.request(ctx, hex.EncodeToString(key[:]), request, "note", body)
	if e != nil {
		return "", e
	}
	var out struct {
		Status string `json:"status"`
	}
	e = json.Unmarshal(raw, &out)
	return out.Status, e
}
func (c Client) request(parent context.Context, id, request, kind, body string) ([]byte, error) {
	ctx, stop := context.WithTimeout(parent, 8*time.Second)
	defer stop()
	if c.DB == nil {
		return nil, errors.New("agenda_unavailable")
	}
	_, e := c.DB.Pool.Exec(ctx, `INSERT INTO bia_go.agenda_requests(id,voice_request_id,kind,body) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, id, request, kind, body)
	if e != nil {
		return nil, e
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		var raw []byte
		var match bool
		e = c.DB.Pool.QueryRow(ctx, `SELECT result,voice_request_id=$2 AND kind=$3 AND body=$4 FROM bia_go.agenda_requests WHERE id=$1`, id, request, kind, body).Scan(&raw, &match)
		if e != nil {
			return nil, e
		}
		if !match {
			return nil, store.ErrConflict
		}
		if raw != nil {
			return raw, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
}
