package agent

import (
	"regexp"
	"strings"
	"unicode"

	"arkom/bia/internal/domain"
)

var repeatedCause = regexp.MustCompile(`(?i)(o que (te |você |voce )?(fez|levou)|me di[zgs]a? o que|por que (acha|achou|voc[eê] acha)|qual (é |e )?o motivo)`)
var conditionalInvite = regexp.MustCompile(`(?i)((seguimos|seguir|avan[çc]ar|reservar|marcar) com esse hor[aá]rio|posso reservar|vamos reservar|seguimos com (o |esse )?primeiro)`)
var unsupportedConfirmation = regexp.MustCompile(`(?i)(posso|vou|quer que eu|te ajudo|te ajudar|ajudo voc[eê]).{0,45}(confirmar|esclarecer|verificar).{0,45}(condi[çc][ãa]o|custo|gratuidade)`)
var customerAction = regexp.MustCompile(`(?i)(\?|\b(responda|responde|escolha|escolhe|confirme|confirma|separe|separa|traga|traz|leve|leva|envie|envia|me mande|me manda|me conte|me conta|me diga|me diz)\b)`)

func specialistQuality(s domain.State, d domain.Decision) string {
	if d.Action != "reply" {
		return ""
	}
	for _, r := range d.Message {
		if unicode.Is(unicode.Cyrillic, r) {
			return "Reescreva integralmente em português brasileiro, sem letras ou palavras de outros alfabetos."
		}
	}
	if s.Dialogue != nil && s.Dialogue.Objection != nil && s.Dialogue.Objection.Clarification != "" && repeatedCause.MatchString(d.Message) {
		return "O motivo já foi informado literalmente em dialogue.objection.clarification. Responda a ele e NÃO volte a perguntar por que a pessoa acha que não se encaixa."
	}
	if s.Dialogue != nil && s.Dialogue.BookingCondition != "" && conditionalInvite.MatchString(d.Message) {
		return "Existe condição de reserva NÃO confirmada. Esclareça a condição sem convidar a seguir com o horário nem reservar. Não prometa obter confirmação de alguém."
	}
	if s.Dialogue != nil && s.Dialogue.BookingCondition != "" && unsupportedConfirmation.MatchString(d.Message) {
		return "Não existe ferramenta para confirmar essa condição com alguém. Informe que a condição não está confirmada e deixe o cliente decidir aguardar ou prosseguir sem essa exigência. Não prometa verificar nem encaminhar sem ação disponível."
	}
	if s.Dialogue != nil && s.Dialogue.Agent == "objections" && s.Dialogue.Assessment.Intent == "objection" && s.Dialogue.BookingCondition == "" && s.Dialogue.Objection != nil && len(s.Dialogue.Objection.Attempts) < 2 && s.Dialogue.Assessment.Readiness != "declined" && s.Dialogue.Assessment.Readiness != "deferred" {
		if strings.Contains(strings.ToLower(d.Message), "se quiser") || !customerAction.MatchString(d.Message) {
			return "Esta objeção ainda tem abertura e não atingiu duas tentativas. Depois de responder ao motivo, termine com UMA pergunta de decisão concreta, sem 'se quiser' nem convite vago. Com causa desconhecida, pergunte apenas a causa; com causa já explicada, convide a avaliar esse processo na reunião. Preserve reserva atual e condições desconhecidas."
		}
	}
	if !domain.SafeReply(d.Message, s, domain.Policy{}) {
		return "Não declare ações concluídas nem use 'reunião confirmada', 'reservei', 'agendei' ou links não autorizados. Sobre custo desconhecido, diga 'não tenho esse custo confirmado', sem formular uma confirmação de reunião."
	}
	for _, v := range []string{"vou te transferir", "sou o agente", "worker", "previous_response_id", "decisions api"} {
		if strings.Contains(strings.ToLower(d.Message), v) {
			return "A resposta é somente da Bia para o cliente. Remova apresentação de outro agente, transferência e termos internos."
		}
	}
	return ""
}

func specialistQualityFallback(s domain.State) domain.Decision {
	text := "Qual é sua dúvida sobre a conversa com o time?"
	if s.Dialogue != nil && s.Dialogue.BookingCondition != "" {
		text = "Ainda não tenho essa condição confirmada. Você prefere aguardar essa informação ou seguir mesmo assim?"
	} else if s.Dialogue != nil && s.Dialogue.Objection != nil && s.Dialogue.Objection.Clarification != "" {
		text = "A conversa com o time pode avaliar se faz sentido aplicar IA no seu caso. Você quer seguir com essa avaliação?"
	}
	return domain.Decision{Action: "reply", Message: text, Reason: "specialist_quality_fallback"}
}
