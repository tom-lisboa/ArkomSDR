package domain

import "time"

// Customer is the shared business context for text and voice. Unknown revenue
// and missing meetings remain null, rather than becoming zero or invented dates.
type Customer struct {
	Company     string     `json:"nome_empresa"`
	Name        string     `json:"nome_pessoa"`
	Revenue     *int64     `json:"faturamento"`
	MeetingDate *time.Time `json:"data_reuniao"`
	TimeZone    string     `json:"fuso_horario"`
}

func CustomerContext(s State, b *Booking, p Policy) Customer {
	c := Customer{Company: s.Profile.Company, Name: s.Profile.Name, Revenue: s.Profile.Revenue, TimeZone: p.Zone(s).String()}
	if b != nil {
		start := b.Start.In(p.Zone(s))
		c.MeetingDate = &start
	}
	return c
}

type MeetingBriefing struct {
	Consultant       string `json:"consultor"`
	ConsultantGender string `json:"consultor_genero"`
	Format           string `json:"formato"`
	Agenda           string `json:"pauta"`
	Context          string `json:"contexto"`
	Environment      string `json:"ambiente"`
}
type MeetingContext struct {
	Status   string          `json:"status"`
	Customer Customer        `json:"cliente"`
	Booking  *Booking        `json:"reuniao"`
	Briefing MeetingBriefing `json:"briefing"`
	Goal     string          `json:"objetivo_informado"`
	Now      time.Time       `json:"agora"`
}
