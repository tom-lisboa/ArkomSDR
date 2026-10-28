package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var weekdays = []string{"domingo", "segunda-feira", "terça-feira", "quarta-feira", "quinta-feira", "sexta-feira", "sábado"}

func SlotLabel(slot Slot, zone *time.Location) string {
	t := slot.Start.In(zone)
	hour := fmt.Sprintf("%dh", t.Hour())
	if t.Minute() != 0 {
		hour += fmt.Sprintf("%02d", t.Minute())
	}
	return weekdays[t.Weekday()] + ", " + t.Format("02/01") + ", às " + hour
}

var choiceClock = regexp.MustCompile(`\b([01]?\d|2[0-3])(?:[:h]([0-5]\d)|h\b| horas?\b)`)
var choiceDate = regexp.MustCompile(`\b([0-3]?\d)/([01]?\d)(?:/(20\d\d))?\b`)
var negativeChoice = regexp.MustCompile(`\b(nao|talvez|ainda|depois eu|vou ver|vou olhar|qual|quais)\b`)
var ordinalFirst = regexp.MustCompile(`\b(primeir[oa]|opcao 1|1a opcao|1ª opcao|1º horario)\b`)
var ordinalSecond = regexp.MustCompile(`\b(segundo|segunda opcao|segunda alternativa|opcao 2|2a opcao|2ª opcao|2º horario)\b|^a segunda[.! ]*$`)

func normalizedChoice(text string) string {
	return strings.NewReplacer("á", "a", "à", "a", "ã", "a", "â", "a", "é", "e", "ê", "e", "í", "i", "ó", "o", "õ", "o", "ô", "o", "ú", "u", "ç", "c").Replace(strings.ToLower(strings.TrimSpace(text)))
}

// Selection must independently resolve to exactly one of the delivered offers.
// The model's chosen ID alone is never authorization to reserve a meeting.
func selectedOffer(text string, offered []Slot, zone *time.Location, now time.Time) (string, bool) {
	t := normalizedChoice(text)
	if negativeChoice.MatchString(t) {
		return "", false
	}
	ordinal := -1
	if ordinalFirst.MatchString(t) {
		ordinal = 0
	}
	if ordinalSecond.MatchString(t) {
		if ordinal >= 0 {
			return "", false
		}
		ordinal = 1
	}
	clocks := choiceClock.FindAllStringSubmatch(t, -1)
	dates := choiceDate.FindAllStringSubmatch(t, -1)
	if len(clocks) > 1 || len(dates) > 1 {
		return "", false
	}
	day := -1
	if ordinal < 0 {
		for i, word := range []string{"domingo", "segunda", "terca", "quarta", "quinta", "sexta", "sabado"} {
			if strings.Contains(t, word) {
				if day >= 0 {
					return "", false
				}
				day = i
			}
		}
	}
	var relative string
	if strings.Contains(t, "amanha") {
		relative = now.In(zone).AddDate(0, 0, 1).Format("2006-01-02")
	}
	if strings.Contains(t, "hoje") {
		if relative != "" {
			return "", false
		}
		relative = now.In(zone).Format("2006-01-02")
	}
	explicitID := ""
	for _, slot := range offered {
		for _, word := range strings.Fields(text) {
			if strings.EqualFold(strings.Trim(word, ",.!?()"), slot.ID) {
				if explicitID != "" && explicitID != slot.ID {
					return "", false
				}
				explicitID = slot.ID
			}
		}
	}
	selected := ""
	for i, slot := range offered {
		local := slot.Start.In(zone)
		directID := explicitID == slot.ID
		if explicitID != "" && !directID {
			continue
		}
		if !directID && ordinal < 0 && len(clocks) == 0 && len(dates) == 0 && day < 0 && relative == "" {
			continue
		}
		if ordinal >= 0 && ordinal != i {
			continue
		}
		if day >= 0 && int(local.Weekday()) != day {
			continue
		}
		if relative != "" && local.Format("2006-01-02") != relative {
			continue
		}
		if len(clocks) == 1 {
			h, _ := strconv.Atoi(clocks[0][1])
			m, _ := strconv.Atoi(clocks[0][2])
			if local.Hour() != h || local.Minute() != m {
				continue
			}
		}
		if len(dates) == 1 {
			d, _ := strconv.Atoi(dates[0][1])
			m, _ := strconv.Atoi(dates[0][2])
			y, _ := strconv.Atoi(dates[0][3])
			if local.Day() != d || int(local.Month()) != m || (y != 0 && local.Year() != y) {
				continue
			}
		}
		if selected != "" {
			return "", false
		}
		selected = slot.ID
	}
	return selected, selected != ""
}
