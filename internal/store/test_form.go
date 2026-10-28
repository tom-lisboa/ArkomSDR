package store

import (
	"arkom/bia/internal/domain"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"regexp"
	"strings"
	"time"
)

// No message or lead mutation happens until the recipient sends the pairing code.
var ErrTestFormLimit = errors.New("test_form_rate_limit")
var formCodePattern = regexp.MustCompile(`\bARKOM-([a-f0-9]{32})\b`)

func (d *DB) RegisterTestForm(ctx context.Context, id string, f domain.Form) (code string, duplicate bool, err error) {
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return "", false, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('bia:test-form-limit',0))`); e != nil {
		return "", false, e
	}
	raw, _ := json.Marshal(f)
	fp := hash(raw)
	var old string
	e = tx.QueryRow(ctx, `SELECT code,fingerprint FROM bia_go.test_forms WHERE id=$1`, id).Scan(&code, &old)
	if e == nil {
		if old != fp {
			return "", false, ErrConflict
		}
		return code, true, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return "", false, e
	}
	var total, phone int
	if e = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE phone=$1) FROM bia_go.test_forms WHERE created_at>now()-interval '1 hour'`, testPhoneKey(f.Phone)).Scan(&total, &phone); e != nil {
		return "", false, e
	}
	if total >= 60 || phone >= 10 {
		return "", false, ErrTestFormLimit
	}
	code = token()
	_, e = tx.Exec(ctx, `INSERT INTO bia_go.test_forms(id,code,phone,profile,fingerprint) VALUES($1,$2,$3,$4,$5)`, id, code, testPhoneKey(f.Phone), raw, fp)
	if e != nil {
		return "", false, e
	}
	return code, false, tx.Commit(ctx)
}

// Brazilian WhatsApp may report the mobile number without the ninth digit.
// This equivalence is used ONLY with possession of a one-use form code.
func testPhoneKey(s string) string {
	if strings.HasPrefix(s, "+55") && len(s) == 14 && s[5] == '9' && s[6] >= '6' && s[6] <= '9' {
		return s[:5] + s[6:]
	}
	return s
}
func claimTestForm(ctx context.Context, tx pgx.Tx, ev domain.Event, mode string, seed *domain.State) error {
	if seed == nil {
		return nil
	}
	match := formCodePattern.FindStringSubmatch(ev.Text)
	if len(match) != 2 {
		return nil
	}
	var raw []byte
	var id, phone string
	var claimed *string
	var expires time.Time
	e := tx.QueryRow(ctx, `SELECT id,phone,profile,claimed_by,expires_at FROM bia_go.test_forms WHERE code=$1 FOR UPDATE`, match[1]).Scan(&id, &phone, &raw, &claimed, &expires)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if claimed != nil || !expires.After(time.Now()) || phone != testPhoneKey(seed.Profile.Phone) {
		return nil
	}
	var f domain.Form
	if e = json.Unmarshal(raw, &f); e != nil {
		return e
	}
	f.Phone = seed.Profile.Phone
	f.ConversationID = seed.Profile.ConversationID
	formEvent := domain.Event{ID: "test-form-" + id, LeadID: ev.LeadID, Kind: "form.submitted", At: ev.At, Form: &f}
	if e = domain.ValidateEvent(formEvent); e != nil {
		return e
	}
	raw, _ = json.Marshal(formEvent)
	fpEvent := formEvent
	fpEvent.At = time.Time{}
	fp, _ := json.Marshal(fpEvent)
	// The existing per-lead lease orders this profile update before the next Run.
	_, e = tx.Exec(ctx, `INSERT INTO bia_go.jobs(id,lead_id,kind,mode,payload,fingerprint,due_at) VALUES($1,$2,'event',$3,$4,$5,now()) ON CONFLICT DO NOTHING`, formEvent.ID, ev.LeadID, mode, raw, hash(fp))
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE bia_go.test_forms SET claimed_by=$2,claimed_at=now() WHERE id=$1`, id, ev.LeadID)
	return e
}
