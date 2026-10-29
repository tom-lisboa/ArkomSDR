package management

import (
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

func ratio(n, d int64) *float64 {
	if d == 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	from, to := now.AddDate(0, 0, -30), now
	var err error
	if raw := r.URL.Query().Get("from"); raw != "" {
		from, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			fail(w, 422, "invalid_from")
			return
		}
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		to, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			fail(w, 422, "invalid_to")
			return
		}
	}
	if !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		fail(w, 422, "range_must_be_1_to_366_days")
		return
	}
	zone := r.URL.Query().Get("timezone")
	if zone == "" {
		zone = s.Policy.TimeZone
	}
	if zone == "" {
		zone = "America/Cuiaba"
	}
	if _, err = time.LoadLocation(zone); err != nil {
		fail(w, 422, "invalid_timezone")
		return
	}
	tx, err := s.DB.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if readError(w, err) {
		return
	}
	defer tx.Rollback(r.Context())
	var total, open, human, paused, hot, attention int64
	err = tx.QueryRow(r.Context(), leadCTE()+`SELECT count(*),count(*) FILTER(WHERE status='open' AND automation<>'opted_out'),count(*) FILTER(WHERE status='open' AND automation<>'opted_out' AND COALESCE((state->>'human')::boolean,false)),count(*) FILTER(WHERE status='open' AND automation<>'opted_out' AND COALESCE((state->>'paused')::boolean,false)),count(*) FILTER(WHERE status='open' AND score>=70),count(*) FILTER(WHERE status='open' AND automation<>'opted_out' AND (failed_jobs>0 OR automation='human')) FROM scored`, now).Scan(&total, &open, &human, &paused, &hot, &attention)
	if readError(w, err) {
		return
	}
	stages := map[string]int64{}
	rows, err := tx.Query(r.Context(), leadCTE()+`SELECT pipeline_stage,count(*) FROM scored WHERE status='open' AND automation<>'opted_out' GROUP BY pipeline_stage`, now)
	if readError(w, err) {
		return
	}
	for rows.Next() {
		var stage string
		var n int64
		if err = rows.Scan(&stage, &n); err != nil {
			break
		}
		stages[stage] = n
	}
	rowErr := rows.Err()
	rows.Close()
	if readError(w, err) || readError(w, rowErr) {
		return
	}
	var cohort, booked, won, bookingSamples, salesSamples, contactSamples int64
	var avgBooking, p50Booking, avgSales, p50Sales, avgContact *float64
	err = tx.QueryRow(r.Context(), `WITH first_booking AS(SELECT lead_id,min(created_at) AS at FROM bia_go.bookings GROUP BY lead_id),cohort AS(SELECT (l.state->>'created_at')::timestamptz AS created_at,(l.state->>'first_contact_at')::timestamptz AS first_contact_at,b.at AS booked_at,m.first_won_at FROM bia_go.leads l LEFT JOIN first_booking b ON b.lead_id=l.id LEFT JOIN bia_go.lead_management m ON m.lead_id=l.id WHERE (l.state->>'created_at')::timestamptz>=$1 AND (l.state->>'created_at')::timestamptz<$2)
 SELECT count(*),count(booked_at),count(first_won_at),count(*) FILTER(WHERE booked_at>=created_at),count(*) FILTER(WHERE first_won_at>=created_at),count(*) FILTER(WHERE first_contact_at>=created_at),
 avg(extract(epoch FROM booked_at-created_at)) FILTER(WHERE booked_at>=created_at),percentile_cont(0.5) WITHIN GROUP(ORDER BY extract(epoch FROM booked_at-created_at)) FILTER(WHERE booked_at>=created_at),
 avg(extract(epoch FROM first_won_at-created_at)) FILTER(WHERE first_won_at>=created_at),percentile_cont(0.5) WITHIN GROUP(ORDER BY extract(epoch FROM first_won_at-created_at)) FILTER(WHERE first_won_at>=created_at),
 avg(extract(epoch FROM first_contact_at-created_at)) FILTER(WHERE first_contact_at>=created_at) FROM cohort`, from, to).Scan(&cohort, &booked, &won, &bookingSamples, &salesSamples, &contactSamples, &avgBooking, &p50Booking, &avgSales, &p50Sales, &avgContact)
	if readError(w, err) {
		return
	}
	var attended, noShow, unknown, cancelled, upcoming, completed, completedKnown int64
	err = tx.QueryRow(r.Context(), `WITH meetings AS(SELECT b.cancelled_at,(b.payload->>'end')::timestamptz AS ends_at,COALESCE(a.outcome,CASE WHEN l.state#>>'{booking,id}'=b.id THEN NULLIF(l.state#>>'{booking,outcome}','') END) AS outcome FROM bia_go.bookings b JOIN bia_go.leads l ON l.id=b.lead_id LEFT JOIN bia_go.meeting_attendance a ON a.booking_id=b.id WHERE (b.payload->>'start')::timestamptz>=$1 AND (b.payload->>'start')::timestamptz<$2)
 SELECT count(*) FILTER(WHERE cancelled_at IS NULL AND outcome='attended'),count(*) FILTER(WHERE cancelled_at IS NULL AND outcome='no_show'),count(*) FILTER(WHERE cancelled_at IS NULL AND ends_at<=$3 AND outcome IS NULL),count(*) FILTER(WHERE cancelled_at IS NOT NULL),count(*) FILTER(WHERE cancelled_at IS NULL AND ends_at>$3 AND outcome IS NULL),count(*) FILTER(WHERE cancelled_at IS NULL AND ends_at<=$3),count(*) FILTER(WHERE cancelled_at IS NULL AND ends_at<=$3 AND outcome IS NOT NULL) FROM meetings`, from, to, now).Scan(&attended, &noShow, &unknown, &cancelled, &upcoming, &completed, &completedKnown)
	if readError(w, err) {
		return
	}
	var sent, manual, failed, scheduled int64
	err = tx.QueryRow(r.Context(), `SELECT count(*) FILTER(WHERE status='done' AND `+deliveredSQL+`),count(*) FILTER(WHERE status='done' AND `+deliveredSQL+` AND COALESCE((payload->>'manual_reminder')::boolean,false)),count(*) FILTER(WHERE status IN ('dead','uncertain')),count(*) FILTER(WHERE status='pending') FROM bia_go.jobs WHERE kind='effect' AND payload->>'purpose' IN `+followupPurposes+` AND COALESCE(completed_at,due_at)>=$1 AND COALESCE(completed_at,due_at)<$2`, from, to).Scan(&sent, &manual, &failed, &scheduled)
	if readError(w, err) {
		return
	}
	series := []map[string]any{}
	rows, err = tx.Query(r.Context(), `WITH days AS(SELECT d::date AS day FROM generate_series(($1::timestamptz AT TIME ZONE $3)::date,(($2::timestamptz-interval '1 microsecond') AT TIME ZONE $3)::date,interval '1 day') d),leads AS(SELECT ((state->>'created_at')::timestamptz AT TIME ZONE $3)::date AS day,count(*) AS n FROM bia_go.leads WHERE (state->>'created_at')::timestamptz>=$1 AND (state->>'created_at')::timestamptz<$2 GROUP BY 1),booked AS(SELECT (created_at AT TIME ZONE $3)::date AS day,count(*) AS n FROM bia_go.bookings WHERE created_at>=$1 AND created_at<$2 GROUP BY 1),won AS(SELECT (first_won_at AT TIME ZONE $3)::date AS day,count(*) AS n FROM bia_go.lead_management WHERE first_won_at>=$1 AND first_won_at<$2 GROUP BY 1)
 SELECT to_char(d.day,'YYYY-MM-DD'),COALESCE(l.n,0),COALESCE(b.n,0),COALESCE(w.n,0) FROM days d LEFT JOIN leads l USING(day) LEFT JOIN booked b USING(day) LEFT JOIN won w USING(day) ORDER BY d.day`, from, to, zone)
	if readError(w, err) {
		return
	}
	for rows.Next() {
		var day string
		var leads, bookings, sales int64
		if err = rows.Scan(&day, &leads, &bookings, &sales); err != nil {
			break
		}
		series = append(series, map[string]any{"date": day, "leads_created": leads, "reservations_created": bookings, "first_sales_recorded": sales})
	}
	rowErr = rows.Err()
	rows.Close()
	if readError(w, err) || readError(w, rowErr) {
		return
	}
	if readError(w, tx.Commit(r.Context())) {
		return
	}
	write(w, 200, map[string]any{
		"as_of": now, "period": map[string]any{"from_inclusive": from, "to_exclusive": to, "timezone": zone},
		"snapshot":       map[string]any{"total_leads": total, "open_leads": open, "human_open_leads": human, "paused_open_leads": paused, "hot_open_leads": hot, "attention_required": attention, "open_by_stage": stages},
		"lead_cohort":    map[string]any{"leads_created": cohort, "ever_booked_leads": booked, "ever_won_leads": won, "booking_conversion_rate": ratio(booked, cohort), "sales_conversion_rate": ratio(won, cohort), "time_to_first_booking_seconds": map[string]any{"average": avgBooking, "p50": p50Booking, "sample_size": bookingSamples}, "time_to_first_sale_seconds": map[string]any{"average": avgSales, "p50": p50Sales, "sample_size": salesSamples}, "time_to_first_contact_seconds": map[string]any{"average": avgContact, "sample_size": contactSamples}},
		"meeting_cohort": map[string]any{"attended": attended, "no_show": noShow, "completed_without_outcome": unknown, "cancelled": cancelled, "upcoming_or_in_progress": upcoming, "show_up_rate": ratio(attended, attended+noShow), "show_up_sample_size": attended + noShow, "completed_attendance_coverage": ratio(completedKnown, completed)},
		"followups":      map[string]any{"sent": sent, "manual": manual, "automatic": sent - manual, "failed_or_uncertain": failed, "scheduled": scheduled}, "daily_events": series, "rate_unit": "fraction", "score_version": "engagement-v1",
	})
}
