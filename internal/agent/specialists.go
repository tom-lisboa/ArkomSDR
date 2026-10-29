package agent

import (
	"embed"
	"regexp"
	"strings"
	"time"

	"arkom/bia/internal/domain"
)

const SpecialistPromptVersion = "bia-specialists-v3"

func normalizeAssessment(s domain.State, e domain.Event, a domain.DialogueAssessment) domain.DialogueAssessment {
	set := func(field string, target *string, value string) {
		if *target != value {
			*target = value
			a.Adjusted = append(a.Adjusted, field)
			delete(a.Confidence, field)
		}
	}
	if s.Dialogue != nil && s.Dialogue.BookingCondition != "" && domain.WithdrawsCondition(e.Text) && choiceCommit.MatchString(e.Text) && !domain.ConditionalAcceptance(e.Text) {
		if _, ok := domain.ResolveOfferedSelection(e.Text, s.Offered, (domain.Policy{TimeZone: "America/Cuiaba"}).Zone(s), time.Now().UTC()); ok && (a.Intent == "select" || a.Intent == "schedule" || a.Intent == "clarify") {
			set("intent", &a.Intent, "select")
			set("objection", &a.Objection, "none")
			set("progress", &a.Progress, "resolved")
			set("readiness", &a.Readiness, "ready")
		}
	}
	if domain.ConditionalAcceptance(e.Text) && (a.Intent == "select" || a.Intent == "schedule" || a.Intent == "clarify" || a.Intent == "objection") {
		set("intent", &a.Intent, "clarify")
		if a.Objection == "none" || a.Objection == "" {
			set("objection", &a.Objection, "unknown")
		}
		set("readiness", &a.Readiness, "considering")
		set("progress", &a.Progress, "open")
	}
	if a.Intent == "objection" && a.Objection == "none" && a.Cause == "none" {
		set("intent", &a.Intent, "value")
		if a.Readiness == "ready" && s.Dialogue != nil && s.Dialogue.Objection != nil {
			set("progress", &a.Progress, "resolved")
		}
	}
	if a.Intent == "objection" {
		if a.Readiness == "declined" {
			set("readiness", &a.Readiness, "considering")
		}
		if a.Progress == "declined" || a.Progress == "resolved" {
			set("progress", &a.Progress, "open")
		}
	}
	if a.Intent == "defer" {
		set("readiness", &a.Readiness, "deferred")
		if s.Dialogue != nil && s.Dialogue.Objection != nil {
			set("progress", &a.Progress, "deferred")
		}
	}
	if a.Intent == "decline" || a.Intent == "opt_out" {
		set("readiness", &a.Readiness, "declined")
		set("progress", &a.Progress, "declined")
	}
	if (a.Intent == "schedule" || a.Intent == "select") && s.Dialogue != nil && s.Dialogue.Objection != nil && !domain.ConditionalAcceptance(e.Text) {
		set("progress", &a.Progress, "resolved")
	}
	if s.Dialogue != nil && s.Dialogue.BookingCondition != "" && !domain.WithdrawsCondition(e.Text) && (a.Intent == "schedule" || a.Intent == "select") {
		kind := "unknown"
		if s.Dialogue.Objection != nil {
			kind = s.Dialogue.Objection.Kind
		}
		set("intent", &a.Intent, "clarify")
		set("objection", &a.Objection, kind)
		set("progress", &a.Progress, "open")
		set("readiness", &a.Readiness, "considering")
	}
	return a
}

//go:embed prompts/*.txt
var specialistFiles embed.FS

func specialistInstructions(brand, id string) string {
	common, _ := specialistFiles.ReadFile("prompts/common.txt")
	job, err := specialistFiles.ReadFile("prompts/" + id + ".txt")
	if err != nil {
		job, _ = specialistFiles.ReadFile("prompts/persuasion.txt")
	}
	return strings.ReplaceAll(string(common), "{{brand}}", brand) + "\n\n" + string(job)
}

func activeMeeting(s domain.State, now time.Time) bool {
	return domain.HasActiveMeeting(s, now)
}

// The registry is closed; Decisions recommends conversational labels, not agent
// names, tools or executable operations. The current booking is independent.
func SelectAssessment(s domain.State, e domain.Event, a domain.DialogueAssessment, now time.Time) string {
	switch a.Intent {
	case "select", "reschedule", "cancel", "confirm":
		return "scheduling"
	case "objection":
		return "objections"
	case "clarify":
		if a.Objection != "none" && a.Objection != "" {
			return "objections"
		}
		if len(s.Offered) > 0 {
			return "scheduling"
		}
	case "schedule":
		if activeMeeting(s, now) {
			return "attendance"
		}
		return "scheduling"
	}
	if activeMeeting(s, now) {
		return "attendance"
	}
	if s.Stage == "no_show" {
		return "recovery"
	}
	if a.Source != "decisions" {
		t := strings.ToLower(e.Text)
		for _, v := range []string{"não é para mim", "nao e para mim", "não serve", "nao serve", "empresa pequena", "caro", "sem tempo", "já uso", "ja uso"} {
			if strings.Contains(t, v) {
				return "objections"
			}
		}
		if s.Dialogue != nil && s.Dialogue.Objection != nil && (s.Dialogue.Objection.Status == "open" || s.Dialogue.Objection.Status == "clarifying") {
			return "objections"
		}
		if Select(s, e) == "scheduling" {
			return "scheduling"
		}
	}
	return "persuasion"
}

func specialistActions(id string, a domain.DialogueAssessment) []string {
	common := []string{"reply", "handoff", "opt_out", "pause"}
	if id == "scheduling" {
		return []string{"reply", "offer_slots", "book", "reschedule", "cancel", "confirm", "pause", "opt_out", "handoff"}
	}
	if id == "attendance" {
		if a.Intent == "confirm" {
			return append(common, "confirm")
		}
		return common
	}
	if id == "objections" && a.Progress != "resolved" {
		return common
	}
	return append(common, "offer_slots")
}

// Defence in depth after typed output: acknowledgments and commercial hesitation
// are not authority to mutate an existing booking or pause every reminder.
func constrainSpecialist(s domain.State, e domain.Event, d domain.Decision, a domain.DialogueAssessment, now time.Time) domain.Decision {
	if a.Intent == "human" {
		return domain.Decision{Action: "handoff", Evidence: e.Text, Reason: "customer_requested_human"}
	}
	if a.Intent == "pause_contact" {
		return domain.Decision{Action: "pause", Evidence: e.Text, Reason: "customer_requested_pause"}
	}
	if a.Intent == "select" && !domain.ConditionalAcceptance(e.Text) && (s.Dialogue == nil || s.Dialogue.BookingCondition == "" || domain.WithdrawsCondition(e.Text)) {
		if id, ok := domain.ResolveOfferedSelection(e.Text, s.Offered, (domain.Policy{TimeZone: "America/Cuiaba"}).Zone(s), now); ok {
			action := "book"
			if s.Booking != nil {
				action = "reschedule"
			}
			return domain.Decision{Action: action, SlotID: id, Evidence: e.Text, Reason: "explicit_offered_selection"}
		}
	}
	if a.Intent == "reschedule" && !domain.ConditionalAcceptance(e.Text) && (s.Dialogue == nil || s.Dialogue.BookingCondition == "" || domain.WithdrawsCondition(e.Text)) {
		if id, ok := domain.ResolveOfferedSelection(e.Text, s.Offered, (domain.Policy{TimeZone: "America/Cuiaba"}).Zone(s), now); ok {
			return domain.Decision{Action: "reschedule", SlotID: id, Evidence: e.Text, Reason: "explicit_replacement_selection"}
		}
		return domain.Decision{Action: "offer_slots", OfferSlotIDs: d.OfferSlotIDs, Reason: "reschedule_requires_available_options"}
	}
	if domain.MeetingStatus(s, now) == "in_progress" && delayNotice.MatchString(e.Text) && !domain.RescheduleRequested(e.Text) {
		p := domain.Policy{TimeZone: "America/Cuiaba"}
		z := p.Zone(s)
		return domain.Decision{Action: "reply", Message: "Seu horário reservado é de " + s.Booking.Start.In(z).Format("15:04") + " a " + s.Booking.End.In(z).Format("15:04") + " (" + z.String() + "). Você consegue participar nesse intervalo ou prefere remarcar?", Reason: "delay_does_not_authorize_host_availability"}
	}
	if d.Action == "confirm" && a.Intent != "confirm" {
		d.Action = "reply"
		if d.Message == "" {
			d.Message = "Combinado."
		}
		d.Reason = "presence_not_explicit"
	}
	if a.Intent == "clarify" && a.Objection == "none" && len(s.Offered) > 1 {
		return domain.Decision{Action: "reply", Message: "Qual dos horários oferecidos você prefere?", Reason: "clarify_offered_slot"}
	}
	if a.Intent == "defer" || a.Intent == "decline" {
		text := "Combinado, fico no aguardo."
		if a.Intent == "decline" {
			text = "Tudo bem. Obrigada por me avisar."
		}
		return domain.Decision{Action: "reply", Message: text, Reason: "respect_" + a.Intent}
	}
	if d.Action == "offer_slots" && offerDetails.MatchString(d.Message) {
		d.Message = ""
	}
	if (a.Intent == "clarify" || a.Intent == "objection") && (d.Action == "book" || d.Action == "reschedule" || d.Action == "offer_slots") && a.Progress != "resolved" {
		d.Action = "reply"
		d.SlotID = ""
		d.OfferSlotIDs = nil
		if strings.TrimSpace(d.Message) == "" {
			d.Message = "O que mais pesa nessa decisão para você?"
		}
	}
	rescheduling := a.Intent == "reschedule" || (a.Intent == "select" && s.Stage == "choosing_slot" && len(s.Offered) > 0)
	if activeMeeting(s, now) && !rescheduling && (d.Action == "offer_slots" || d.Action == "book" || d.Action == "reschedule") {
		p := domain.Policy{TimeZone: "America/Cuiaba"}
		return domain.Decision{Action: "reply", Message: "Sua reunião está marcada para " + s.Booking.Start.In(p.Zone(s)).Format("02/01 às 15:04") + " (" + p.Zone(s).String() + "). Pode me dizer sua dúvida sobre essa conversa?", Reason: "keep_current_booking"}
	}
	for _, v := range []string{"vou te transferir", "vou transferir você", "sou o agente", "agente de objeções", "worker", "previous_response_id", "decisions api"} {
		if strings.Contains(strings.ToLower(d.Message), v) {
			return domain.Decision{Action: "reply", Message: "Continuo por aqui. Qual é sua dúvida sobre a conversa com o time?", Reason: "internal_routing_not_customer_content"}
		}
	}
	return d
}

var offerDetails = regexp.MustCompile(`(?i)(\b[0-9]{1,2}(:[0-9]{2}|h([0-9]{2})?| horas?)\b|[0-9]{1,2}/[0-9]{1,2}|https?://)`)
var delayNotice = regexp.MustCompile(`(?i)(atras|vou entrar depois|vou chegar depois)`)
var choiceCommit = regexp.MustCompile(`(?i)(pode (ser|marcar|agendar|reservar)|quero (o|a)|escolho|fico com|confirma)`)
