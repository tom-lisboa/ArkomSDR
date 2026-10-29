package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReplyPartsPreserveContentAndLinks(t *testing.T) {
	texts := []string{
		"Bom dia! Como posso ajudar?",
		"Recebi sua mensagem e entendi que você quer organizar o atendimento da sua empresa para responder melhor aos contatos.\n\nPodemos conversar sobre o problema que mais toma tempo hoje e avaliar um próximo passo concreto.\n\nQual é a principal dificuldade que você enfrenta no atendimento?",
		strings.Repeat("Informação útil sobre o atendimento. ", 20) + " Veja https://example.com/agenda?utm_source=whatsapp&dia=sexta.",
		"Veja as opções:\n1. " + strings.Repeat("opção de horário ", 15) + "\n2. Outro horário.",
	}
	for _, text := range texts {
		parts := SplitReply(text)
		if len(parts) < 1 || len(parts) > 3 {
			t.Fatal(parts)
		}
		if strings.Join(strings.Fields(strings.Join(parts, " ")), " ") != strings.Join(strings.Fields(text), " ") {
			t.Fatal("changed content", parts)
		}
		for _, p := range parts {
			if !utf8.ValidString(p) || strings.TrimSpace(p) == "" {
				t.Fatal("invalid part")
			}
		}
		if strings.Contains(text, "https://") && !strings.Contains(strings.Join(parts, "|"), "https://example.com/agenda?utm_source=whatsapp&dia=sexta.") {
			t.Fatal("split URL")
		}
	}
	if len(SplitReply(texts[0])) != 1 || len(SplitReply(texts[1])) != 3 || len(SplitReply(texts[3])) != 1 {
		t.Fatal("unexpected conversational boundaries")
	}
}

func TestSlotOfferAndActionStayInFinalBubble(t *testing.T) {
	offer := SlotOfferPrefix + "quinta-feira, 29/10 às 14:00 ou sexta-feira, 30/10 às 09:30. Qual você escolhe: o primeiro ou o segundo?"
	text := "Entendi sua dificuldade. Vamos avaliar esse processo com o time e encontrar o próximo passo.\n\n" + offer
	parts := SplitReply(text)
	if len(parts) != 2 || parts[1] != offer {
		t.Fatal("split actual dates from button CTA", parts)
	}
}
