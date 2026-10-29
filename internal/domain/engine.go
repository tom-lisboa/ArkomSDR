package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var stopRE = regexp.MustCompile(`(?im)(pare de (me )?(ligar|mandar|enviar)|n[ãa]o (me )?ligue|n[ãa]o quero (mais )?(ligaç[õo]es|ligacoes|chamadas)|não (me )?(mande|envie)|nao (me )?(mande|envie)|remova meu|exclua meu|não quero (mais )?receber|nao quero (mais )?receber|^sair$|^stop$)`)

func IsOptOut(text string) bool { return stopRE.MatchString(strings.TrimSpace(text)) }

var cancelRE = regexp.MustCompile(`(?i)^(por favor[, ]*)?(quero |pode |preciso )?(cancelar|cancele|cancela)( minha| a| essa| esta)? (reunião|reuniao|call|reserva|vaga)[.! ]*$`)
var confirmRE = regexp.MustCompile(`(?i)^(sim[,! ]*)?(confirmo( minha presença| minha presenca)?|estarei (lá|la|presente)|vou participar|presença confirmada|presenca confirmada)[.! ]*$`)
var casualAckRE = regexp.MustCompile(`(?i)^(claro|ok|okay|certo|combinado|fechou|beleza|perfeito)[.! ]*$`)
var bareYesRE = regexp.MustCompile(`(?i)^(sim|s)[.! ]*$`)

// Reduce is deterministic: it reads no clock, database or network. The planner
// proposes a decision; booking, permission and delivery invariants live here.
func Reduce(s State, e Event, d Decision, slots []Slot, p Policy, now time.Time) Result {
	if s.ID == "" {
		s = State{ID: e.LeadID, Owner: "shadow", Stage: "new", CreatedAt: now}
	}
	r := Result{State: s, Decision: d}
	s = r.State
	add := func(kind, purpose, text string, due, expires time.Time) *Effect {
		fx := Effect{ID: fmt.Sprintf("%s:%d", e.ID, len(r.Effects)), LeadID: s.ID, Kind: kind, Purpose: purpose, Text: text, Due: due, Expires: expires}
		r.Effects = append(r.Effects, fx)
		return &r.Effects[len(r.Effects)-1]
	}
	message := func(purpose, text string) {
		add("send", purpose, text, now, now.Add(30*time.Minute))
	}
	handoff := func(reason string) {
		s.Human = true
		s.Reasons = append(s.Reasons, reason)
		add("crm", "handoff", reason, now, now.Add(7*24*time.Hour))
	}
	finish := func() Result {
		if e.Context != nil {
			for i := range r.Effects {
				r.Effects[i].ConversationID = e.Context.ConversationID
				r.Effects[i].ConversationVersion = e.Context.Version
			}
		}
		for i := range r.Effects {
			if !strings.HasPrefix(r.Effects[i].ID, "fx/") {
				r.Effects[i].ID = "fx/" + r.Effects[i].ID
			}
			if e.Kind == "message.received" && r.Effects[i].Kind != "crm" {
				r.Effects[i].TriggerAt = e.At
			}
		}
		if e.At.After(s.LastEventAt) {
			s.LastEventAt = e.At
		}
		if len(s.Reasons) > 20 {
			s.Reasons = s.Reasons[len(s.Reasons)-20:]
		}
		r.State = s
		return r
	}
	switch e.Kind {
	case "form.submitted", "form.contact_requested":
		// A duplicate registration cannot undo opt-out, ownership or a human takeover.
		first := s.Profile.Phone == ""
		if first {
			s.Profile = *e.Form
			s.Stage = "contact_pending"
		} else {
			// Fresh form data may enrich the profile without restarting outreach.
			f := *e.Form
			if f.Company == "" {
				f.Company = s.Profile.Company
			}
			if f.Email == "" {
				f.Email = s.Profile.Email
			}
			if f.Goal == "" {
				f.Goal = s.Profile.Goal
			}
			if f.TimeZone == "" {
				f.TimeZone = s.Profile.TimeZone
			}
			if f.Revenue == nil {
				f.Revenue = s.Profile.Revenue
			}
			if f.ConversationID == "" {
				f.ConversationID = s.Profile.ConversationID
			}
			if f.ProviderLeadID == "" {
				f.ProviderLeadID = s.Profile.ProviderLeadID
			}
			s.Profile = f
		}
		if e.Kind == "form.contact_requested" && s.Profile.Consent && !s.OptedOut {
			// The form may submit before the page's separate booking request. Give
			// that booking event a short head start so the opening reflects it.
			add("send", "form_contact", FormContactText, now.Add(10*time.Second), now.Add(24*time.Hour)).TriggerAt = e.At
			add("crm", "form_received", "Formulário recebido; contato automático enfileirado.", now, now.Add(24*time.Hour))
		} else if first && s.Profile.Consent && !s.OptedOut {
			// Initial contact is immediate after the explicit form request. Quiet
			// hours apply to later unsolicited follow-ups, not this acknowledgment.
			add("send", "welcome", FormContactText, now, now.Add(15*time.Minute))
			add("crm", "lead_created", "Cadastro recebido; primeiro contato enfileirado.", now, now.Add(24*time.Hour))
		}
	case "ownership.changed":
		s.Owner = e.Owner
	case "contact.sms_consent":
		if e.SMSConsent == nil {
			return finish()
		}
		if *e.SMSConsent && (s.OptedOut || s.Paused || s.Human || !s.Profile.Consent) {
			return finish()
		}
		if *e.SMSConsent && SMSDestination(Form{Phone: s.Profile.Phone, SMSPhone: e.SMSPhone}) == "" {
			return finish()
		}
		s.Profile.SMSConsent = *e.SMSConsent
		s.Profile.SMSPhone = ""
		if *e.SMSConsent {
			s.Profile.SMSPhone = e.SMSPhone
		}
		s.Profile.SMSConsentVersion = "test-form-sms-v1"
		add("crm", "sms_consent", fmt.Sprintf("Preferência SMS: %t; origem: %s", *e.SMSConsent, e.Reason), now, now.Add(24*time.Hour))
	case "contact.opt_out":
		s.OptedOut = true
		s.Paused = true
		s.Stage = "opted_out"
		add("crm", "opt_out", "Contato pediu para não receber mensagens.", now, now.Add(7*24*time.Hour))
	case "human.takeover":
		s.Human = true
	case "human.release":
		s.Human = false
	case "cadence.resume":
		if !s.OptedOut {
			s.Paused = false
		}
	case "schedule.preference":
		s.SchedulePreference = e.Preference
		s.Stage = "awaiting_availability"
		add("crm", "availability_requested", e.Preference, now, now.Add(7*24*time.Hour))
	case "booking.created", "booking.rescheduled":
		if e.At.Before(s.LastBookingEventAt) {
			return finish()
		}
		s.LastBookingEventAt = e.At
		if s.Booking != nil && s.Booking.ID == e.Booking.ID && s.Booking.Start.Equal(e.Booking.Start) {
			return finish()
		}
		if s.Booking != nil {
			s.PreviousBooking = s.Booking
		}
		b := *e.Booking
		s.Booking = &b
		s.Offered = nil
		s.OfferVersion = ""
		s.Stage = "booked"
		if b.Start.After(now) {
			fx := add("send", "booking_confirmation", bookingText(b, p, s), now, b.Start)
			fx.BookingID = b.ID
			fx.BookingStart = b.Start
			for _, before := range p.Reminders {
				if p.MeetingReminders {
					break
				}
				due := b.Start.Add(-before)
				if !due.After(now) {
					continue
				}
				due = p.NextContact(due, s)
				if !due.Before(b.Start) {
					continue
				}
				fx := add("send", "reminder", "", due, minTime(due.Add(20*time.Minute), b.Start))
				fx.BookingID = b.ID
				fx.BookingStart = b.Start
			}
		}
		if p.MeetingReminders {
			r.Effects = append(r.Effects, MeetingSequence(s, now)...)
		}
		if p.SMSReminders {
			r.Effects = append(r.Effects, SMSSequence(s, now)...)
		}
		if p.MeetingPreparation {
			r.Effects = append(r.Effects, PreparationSequence(s, now)...)
		}
		add("crm", "booking", "Reserva confirmada pelo provedor: "+b.ID, now, now.Add(24*time.Hour)).Booking = &b
	case "booking.cancelled":
		if e.At.Before(s.LastBookingEventAt) || s.Booking == nil || s.Booking.ID != e.BookingID {
			return finish()
		}
		s.LastBookingEventAt = e.At
		s.PreviousBooking = s.Booking
		s.Booking = nil
		s.Stage = "cancelled"
		s.OfferVersion = ""
		add("crm", "booking_cancelled", "Cancelamento confirmado pelo provedor.", now, now.Add(24*time.Hour))
	case "attendance.attended", "attendance.no_show":
		if s.Booking == nil || s.Booking.ID != e.BookingID || e.At.Before(s.Booking.Start) {
			return finish()
		}
		if e.At.Before(s.LastAttendanceAt) || s.Booking.Outcome == strings.TrimPrefix(e.Kind, "attendance.") {
			return finish()
		}
		s.LastAttendanceAt = e.At
		b := *s.Booking
		b.Outcome = strings.TrimPrefix(e.Kind, "attendance.")
		s.Booking = &b
		s.Stage = b.Outcome
		add("crm", b.Outcome, "Presença informada pela integração: "+b.Outcome, now, now.Add(24*time.Hour))
		if b.Outcome == "no_show" {
			due := p.NextContact(now, s)
			fx := add("send", "recovery", "Vi que não conseguimos conversar no horário marcado. Aconteceu algum imprevisto? Se quiser retomar, você pode escolher outro horário: "+p.SchedulingURL, due, due.Add(time.Hour))
			fx.BookingID = b.ID
			fx.BookingStart = b.Start
		}
	case "message.received":
		if AutomaticReply(e.Text) {
			return finish()
		}
		if stopRE.MatchString(strings.TrimSpace(e.Text)) {
			s.OptedOut = true
			s.Paused = true
			s.Stage = "opted_out"
			add("crm", "opt_out", "Pedido explícito de saída.", now, now.Add(24*time.Hour))
			return finish()
		}
		if e.At.Before(s.LastInbound) || now.Sub(e.At) > 24*time.Hour {
			return finish()
		}
		s.LastInbound = e.At
		s.OfferVersion = ""
		AppendTurn(&s, "user", e.Text, e.At)
		if s.OptedOut || s.Human {
			return finish()
		}
		if e.Context != nil && e.Context.MediaFailed {
			handoff("media_processing_failed")
			return finish()
		}
		if e.AudioURL != "" && e.Text == "" {
			handoff("audio_without_transcript")
			return finish()
		}
		if confirmRE.MatchString(strings.TrimSpace(e.Text)) {
			d.Action = "confirm"
		}
		if cancelRE.MatchString(strings.TrimSpace(e.Text)) {
			d.Action = "cancel"
		}
		if e.SelectedSlotID != "" {
			d.Action = "book"
			if s.Booking != nil {
				d.Action = "reschedule"
			}
			d.SlotID = e.SelectedSlotID
		}
		r.Decision = d
		switch d.Action {
		case "opt_out":
			if !evidence(e, d) {
				handoff("unproven_opt_out")
				break
			}
			s.OptedOut = true
			s.Paused = true
			s.Stage = "opted_out"
			add("crm", "opt_out", "Saída solicitada: "+d.Evidence, now, now.Add(24*time.Hour))
		case "pause":
			s.Paused = true
			message("reply", "Tudo bem, vou pausar os lembretes. Quando quiser continuar, pode falar comigo por aqui.")
		case "handoff":
			handoff(d.Reason)
		case "confirm":
			if s.Booking == nil || !s.Booking.Start.After(now) {
				offer(&s, &r, e, slots, p, now)
				break
			}
			if casualAckRE.MatchString(strings.TrimSpace(e.Text)) {
				message("reply", "Combinado. "+bookingText(*s.Booking, p, s))
				break
			}
			if !confirmRE.MatchString(strings.TrimSpace(e.Text)) && !(bareYesRE.MatchString(strings.TrimSpace(e.Text)) && lastAssistantAskedAttendance(s)) {
				handoff("ambiguous_confirmation")
				break
			}
			b := *s.Booking
			b.ConfirmedAt = &now
			s.Booking = &b
			message("reply", "Presença confirmada! "+bookingText(b, p, s))
			add("crm", "presence_confirmed", "Lead confirmou presença.", now, now.Add(24*time.Hour))
		case "cancel":
			if !cancelRE.MatchString(strings.TrimSpace(e.Text)) {
				message("reply", "Você quer cancelar a reunião ou prefere encontrar outro horário?")
				break
			}
			if s.Booking == nil || !s.Booking.Start.After(now) {
				message("reply", "Não encontrei uma reserva futura para cancelar.")
				break
			}
			if s.Booking.Group && (s.Booking.SeatID == "" || s.Booking.Start.Sub(now) < 30*time.Minute) {
				handoff("group_seat_cancellation_requires_review")
				break
			}
			fx := add("cancel", "cancel_booking", "", now, now.Add(5*time.Minute))
			fx.Booking = s.Booking
			fx.BookingID = s.Booking.ID
		case "book", "reschedule":
			var chosen *Slot
			for _, slot := range s.Offered {
				if slot.ID == d.SlotID {
					x := slot
					chosen = &x
					break
				}
			}
			// A selection from the authenticated site API is checked against the
			// current calendar, so it need not have been offered on WhatsApp first.
			if chosen == nil && e.SelectedSlotID != "" {
				for _, slot := range slots {
					if slot.ID == e.SelectedSlotID {
						x := slot
						chosen = &x
						break
					}
				}
			}
			if chosen == nil || !chosen.Start.After(now.Add(30*time.Minute)) {
				offer(&s, &r, e, slots, p, now)
				break
			}
			// Resolve the actual inbound message independently. The model may
			// quote JSON labels in evidence; that must not break an unambiguous
			// human choice, nor authorize a different slot.
			if e.SelectedSlotID == "" && !selectionEvidence(e.Text, *chosen, s.Offered, p.Zone(s), now) {
				message("reply", "Para confirmar, qual dos horários oferecidos você escolhe?")
				break
			}
			if e.SelectedSlotID == "" {
				r.Decision.Evidence = e.Text
			}
			if s.Booking != nil && !s.Booking.Group && (s.Booking.CloserID == 0 || chosen.CloserID != s.Booking.CloserID || chosen.Provider != s.Booking.Provider) {
				handoff("reschedule_must_keep_provider_and_closer")
				break
			}
			if s.Booking != nil && s.Booking.Group {
				handoff("group_to_individual_requires_seat_saga")
				break
			}
			fx := add("book", "booking", "", now, now.Add(5*time.Minute))
			fx.Slot = chosen
			fx.Booking = s.Booking
			if s.Booking != nil {
				fx.BookingID = s.Booking.ID
			}
		case "offer_slots":
			offer(&s, &r, e, slots, p, now)
		default:
			text := strings.TrimSpace(d.Message)
			if text == "" {
				offer(&s, &r, e, slots, p, now)
				break
			}
			if !SafeReply(text, s, p) {
				handoff("unsafe_or_unverified_model_reply")
				break
			}
			message("reply", text)
		}
	}
	return finish()
}

func offer(s *State, r *Result, e Event, slots []Slot, p Policy, now time.Time) {
	if s.Booking != nil && s.Booking.Outcome == "" && s.Booking.Start.After(now) && !RescheduleRequested(e.Text) && !(s.Stage == "choosing_slot" && len(s.Offered) > 0) {
		s.Offered = nil
		s.OfferVersion = ""
		s.Stage = "booked"
		r.Effects = append(r.Effects, Effect{ID: e.ID + ":existing-booking", LeadID: s.ID, Kind: "send", Purpose: "reply", Text: bookingText(*s.Booking, p, *s), Due: now, Expires: now.Add(30 * time.Minute), BookingID: s.Booking.ID, BookingStart: s.Booking.Start})
		return
	}
	available := append([]Slot(nil), slots...)
	sort.SliceStable(available, func(i, j int) bool { return available[i].Start.Before(available[j].Start) })
	// Prefer the model's requested IDs only if every ID is in the fresh calendar.
	preferred := []Slot{}
	seenPreferred := map[string]bool{}
	validPreference := len(r.Decision.OfferSlotIDs) > 0 && len(r.Decision.OfferSlotIDs) <= 2
	for _, id := range r.Decision.OfferSlotIDs {
		found := false
		for _, slot := range available {
			if slot.ID == id && !seenPreferred[id] {
				preferred = append(preferred, slot)
				seenPreferred[id] = true
				found = true
				break
			}
		}
		if !found {
			validPreference = false
		}
	}
	if validPreference {
		available = append(preferred, available...)
	}
	s.Offered = nil
	seen := map[string]bool{}
	for _, slot := range available {
		if seen[slot.ID] || !slot.Start.After(now.Add(30*time.Minute)) || slot.Group {
			continue
		}
		if s.Booking != nil && (slot.Provider != s.Booking.Provider || slot.CloserID != s.Booking.CloserID || s.Booking.CloserID == 0) {
			continue
		}
		seen[slot.ID] = true
		s.Offered = append(s.Offered, slot)
		if len(s.Offered) == 2 {
			break
		}
	}
	s.OfferVersion = e.ID
	text := "Não encontrei horários disponíveis na agenda agora. O agendamento ainda não foi feito."
	if e.CalendarUnavailable {
		text = "Não consegui consultar a agenda agora. O agendamento ainda não foi feito."
	}
	if len(s.Offered) == 0 {
		s.Stage = "awaiting_availability"
		if s.Booking != nil {
			text += " Sua reserva atual continua confirmada."
		}
		r.Effects = append(r.Effects, Effect{ID: e.ID + ":no-slots", LeadID: s.ID, Kind: "crm", Purpose: "availability_requested", Text: "Agendamento pendente: " + text, Due: now, Expires: now.Add(24 * time.Hour)})
	} else {
		s.Stage = "choosing_slot"
		parts := []string{}
		for _, slot := range s.Offered {
			parts = append(parts, SlotLabel(slot, p.Zone(*s)))
		}
		text = "Tenho estes horários para o diagnóstico: " + strings.Join(parts, " ou ") + ". Horário de " + p.Zone(*s).String() + ". Qual você escolhe?"
	}
	if intro := strings.TrimSpace(r.Decision.Message); r.Decision.Action == "offer_slots" && intro != "" && !strings.Contains(intro, "?") && len([]rune(intro)) <= 350 && SafeReply(intro, *s, p) {
		text = intro + "\n\n" + text
	}
	r.Effects = append(r.Effects, Effect{ID: e.ID + ":offer", LeadID: s.ID, Kind: "send", Purpose: "reply", Text: text, Due: now, Expires: now.Add(30 * time.Minute)})
	if len(s.Offered) > 0 && !s.Paused && !p.DisableFollowups {
		due := p.NextContact(now.Add(p.FollowupAfter), *s)
		r.Effects = append(r.Effects, Effect{ID: e.ID + ":followup", LeadID: s.ID, Kind: "send", Purpose: "followup", Text: "Quer seguir com o diagnóstico? Posso consultar os horários atualizados por aqui.", Due: due, Expires: due.Add(time.Hour), OfferVersion: e.ID})
	}
}

func evidence(e Event, d Decision) bool {
	return strings.TrimSpace(d.Evidence) != "" && strings.Contains(strings.ToLower(e.Text), strings.ToLower(d.Evidence))
}

func lastAssistantAskedAttendance(s State) bool {
	for i := len(s.History) - 1; i >= 0; i-- {
		if s.History[i].Role != "assistant" {
			continue
		}
		text := strings.ToLower(s.History[i].Text)
		return strings.Contains(text, "confirma sua presença") || strings.Contains(text, "confirmar sua presença") || strings.Contains(text, "confirma sua presenca") || strings.Contains(text, "confirmar sua presenca")
	}
	return false
}
func selectionEvidence(text string, slot Slot, offered []Slot, zone *time.Location, now time.Time) bool {
	id, ok := selectedOffer(text, offered, zone, now)
	return ok && id == slot.ID
}

func firstName(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func bookingText(b Booking, p Policy, s State) string {
	text := "Seu diagnóstico está marcado para " + b.Start.In(p.Zone(s)).Format("02/01/2006 às 15:04") + " (" + p.Zone(s).String() + ")."
	if b.MeetingURL != "" {
		text += " Link da reunião: " + b.MeetingURL
	}
	return text + " Se surgir um imprevisto, me avise para encontrarmos outro horário."
}

var urlRE = regexp.MustCompile(`https?://[^\s<>]+`)
var claimRE = regexp.MustCompile(`(?i)(agendei|reservei|cancelei|remarquei|reunião (está )?confirmada|reuniao (esta )?confirmada|horário garantido|vaga garantida)`)

func SafeReply(text string, s State, p Policy) bool {
	if len([]rune(text)) > 1000 || claimRE.MatchString(text) {
		return false
	}
	allowed := map[string]bool{p.SchedulingURL: true}
	if s.Booking != nil && s.Booking.Outcome == "" {
		allowed[s.Booking.MeetingURL] = true
	}
	for _, u := range urlRE.FindAllString(text, -1) {
		if !allowed[strings.TrimRight(u, ".,!?")] {
			return false
		}
	}
	return true
}

func ValidHTTPS(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// Deliverable is checked again immediately before every effect. A scheduled
// message never bypasses a later opt-out, human takeover or booking replacement.
func Deliverable(s State, fx Effect, now time.Time) (bool, string) {
	if !fx.Expires.IsZero() && !now.Before(fx.Expires) {
		return false, "expired"
	}
	if fx.Kind == "crm" {
		return true, ""
	}
	if !fx.TriggerAt.IsZero() && s.LastInbound.After(fx.TriggerAt) {
		return false, "newer_inbound"
	}
	if s.OptedOut {
		return false, "opted_out"
	}
	if s.Human {
		return false, "human_takeover"
	}
	if fx.BookingID != "" && (s.Booking == nil || s.Booking.ID != fx.BookingID) {
		return false, "stale_booking"
	}
	if fx.Purpose == "meeting_preparation" && s.LastPreparationAt != nil && (s.LastFormContactAt == nil || !s.LastPreparationAt.Before(*s.LastFormContactAt)) {
		return false, "preparation_already_sent"
	}
	if IsMeetingReminder(fx) {
		if fx.Purpose == "meeting_sms" && (!s.Profile.SMSConsent || SMSDestination(s.Profile) == "") {
			return false, "sms_consent_required"
		}
		if !s.Profile.Consent {
			return false, "no_consent"
		}
		if s.Paused {
			return false, "paused"
		}
		if s.Booking == nil || s.Booking.Outcome != "" || !s.Booking.Start.After(now) {
			return false, "meeting_not_upcoming"
		}
	}
	if !fx.BookingStart.IsZero() && (s.Booking == nil || !s.Booking.Start.Equal(fx.BookingStart)) {
		return false, "stale_booking_time"
	}
	if fx.Kind == "send" {
		if fx.Purpose == "booking_confirmation" && s.Booking != nil && s.LastFormBookingID == s.Booking.ID && s.LastFormContactAt != nil && !s.LastFormContactAt.Before(s.LastBookingEventAt) {
			return false, "covered_by_form_opening"
		}
		if fx.Purpose == "form_contact" {
			if s.Paused {
				return false, "paused"
			}
			if s.LastFormContactAt != nil && now.Sub(*s.LastFormContactAt) < 15*time.Minute {
				return false, "form_contact_cooldown"
			}
		}
		if fx.Purpose != "reply" && !s.Profile.Consent {
			return false, "no_consent"
		}
		if fx.Purpose == "welcome" && (s.FirstContactAt != nil || !s.LastInbound.IsZero() || s.Booking != nil) {
			return false, "welcome_superseded"
		}
		if fx.Purpose == "followup" {
			if s.OfferVersion != fx.OfferVersion || s.Booking != nil {
				return false, "followup_superseded"
			}
			if s.LastFollowup != nil && now.Sub(*s.LastFollowup) < 24*time.Hour {
				return false, "followup_cap"
			}
		}
		if s.Paused && (fx.Purpose == "followup" || fx.Purpose == "reminder" || fx.Purpose == "recovery") {
			return false, "paused"
		}
		if fx.Purpose == "reminder" && (s.Booking == nil || s.Booking.Outcome != "" || !s.Booking.Start.After(now)) {
			return false, "meeting_not_upcoming"
		}
		if fx.Purpose == "recovery" && (s.Booking == nil || s.Booking.Outcome != "no_show") {
			return false, "not_no_show"
		}
	}
	return true, ""
}

func Render(s State, fx Effect, p Policy, now time.Time) string {
	if fx.Purpose == "meeting_preparation" {
		return PreparationOpening(s).Text
	}
	if fx.Purpose == "form_contact" || fx.Purpose == "welcome" {
		return ContactOpening(s, p, now).Text
	}
	if fx.Purpose == "meeting_sms" {
		return SMSReminderText(s, p)
	}
	if fx.Purpose == "meeting_call" && s.Booking != nil {
		return "Olá, " + firstName(s.Profile.Name) + "! Sou a Bia, assistente de IA da " + p.Brand + ". " + bookingText(*s.Booking, p, s) + " Posso te ligar para lembrar da reunião e ajudar com o acesso? Se o WhatsApp mostrar a opção de permitir chamadas, toque em permitir."
	}
	if fx.Purpose != "reminder" || s.Booking == nil {
		return fx.Text
	}
	text := bookingText(*s.Booking, p, s)
	if s.Booking.ConfirmedAt == nil && s.Booking.Start.Sub(now) > time.Hour {
		text += " Você confirma sua presença?"
	}
	return text
}

func AutomaticReply(text string) bool {
	t := strings.ToLower(text)
	for _, v := range []string{"mensagem automática", "mensagem automatica", "agradece seu contato", "fora do expediente"} {
		if strings.Contains(t, v) {
			return true
		}
	}
	return false
}

func NeedsPlan(s State, e Event, now time.Time) bool {
	return e.Kind == "message.received" && !s.OptedOut && !s.Human && !AutomaticReply(e.Text) && !stopRE.MatchString(e.Text) && !cancelRE.MatchString(e.Text) && !confirmRE.MatchString(e.Text) && e.SelectedSlotID == "" && !e.At.Before(s.LastInbound) && now.Sub(e.At) <= 24*time.Hour
}
