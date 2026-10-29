# Diagnóstico do formulário e dos disparos

`WEBHOOK` é a URL completa já configurada no formulário, incluindo sua chave. O frontend do formulário é um projeto separado; este repositório fornece o backend e este contrato de integração.

## Mensagens claras para HTTP 409

Os endpoints preservam `error` para compatibilidade e agora também retornam `message`, `retryable` e `next_action`. Exemplo:

```json
{
  "error": "sms_consent_required",
  "message": "Este contato ainda não autorizou lembretes por SMS.",
  "retryable": false,
  "next_action": "request_sms_consent"
}
```

Mostre `message` ao usuário, em vez de "O webhook respondeu 409". Não trate todos os 409 como horário ocupado. Só repita automaticamente erros transitórios, preservando corpo e `Idempotency-Key`, com espera e limite de tentativas. `contact_busy` e `contact_update_pending` retornam `Retry-After: 3` e `retryable: true`.

| Erro | Causa / ação |
|---|---|
| `sms_consent_required` | Falta autorização específica de SMS. `consent: true` sozinho autoriza WhatsApp. Repetir o disparo não registra autorização. |
| `sms_phone_required` | Falta celular válido para SMS, com o nono dígito brasileiro. |
| `contact_update_pending` | A preferência de SMS está na fila; consultar status e aguardar sua aplicação. |
| `contact_blocked` | Verificar pausa, saída solicitada, responsável pelo contato e atendimento humano. Uma saudação ou confirmação ambígua do modelo não deve ativar atendimento humano. |
| `reminder_audio_not_ready` | A gravação ainda não foi instalada; esconder/desabilitar botão de áudio. |
| `future_booking_required` | Sem reunião futura no contexto; concluir o agendamento e aguardar o worker de agenda. |
| `contact_busy` | Há processamento de atendimento; repetir a MESMA reserva após alguns segundos, sem trocar horário nem chave. |
| `lead_already_booked` | A pessoa já agendou, inclusive via IA. Exibir a reserva atual. |
| `voice_request_in_progress` | Já há convite/ligação pendente. Conferir o WhatsApp/status da solicitação existente. |
| `permission_request_limit` | Limite da solicitação de permissão; não tentar contornar com novos UUIDs. |
| `whatsapp_reply_required` | É necessário que a pessoa responda no WhatsApp para testar áudio e chamada. Não se aplica a SMS. |

## Consultar estado antes de habilitar os botões

```http
POST {WEBHOOK}/test-status
Content-Type: application/json
```

```json
{"phone":"+5577999749058","submission_id":"ID_DO_FORMULARIO"}
```

Retorna `booking`, `has_future_booking`, `sms_consent`, `sms_consent_pending`, `sms_phone_valid`, `sms_configured`, `audio_ready`, `call_configured`, `contact_blocked`. Não cria reserva, mensagem ou ligação. O corpo só é aceito quando a submissão ativa, de até 24 horas, pertence ao telefone informado.

Use a reserva atual para atualizar a tela se o contato agendou pela IA. Esses campos são pré-requisitos, não promessa de entrega; os triggers ainda conferem janela, permissões, limites e mudanças da reunião na hora de executar.

Confirmação de presença exige uma resposta explícita. Se o modelo interpretar uma saudação como `confirm`, o motor converte a proposta em resposta de texto, mantém a reunião e não registra presença nem bloqueia os lembretes. A regressão cobre mensagens fragmentadas, execução da resposta e os três botões, incluindo os workers de áudio, SMS e voz com provedores simulados.

## Registrar ou retirar autorização de SMS sem recadastrar

No formulário novo, continue enviando `sms_consent: true` apenas quando a pessoa tiver marcado uma opção como:

> Autorizo receber lembretes desta reunião por SMS no celular informado.

Para um cadastro já enviado, a mesma opção pode acionar:

```http
POST {WEBHOOK}/sms-consent
Content-Type: application/json
Idempotency-Key: NOVO_UUID_PARA_ESTA_ALTERACAO
```

```json
{
  "phone": "+5577999749058",
  "submission_id": "ID_DO_FORMULARIO",
  "sms_consent": true
}
```

Use `false` para retirar a autorização. Não conceda autorização automaticamente ao renderizar a página ou apenas porque a pessoa autorizou WhatsApp. Não envie `true` para todos os contatos.

HTTP 202 confirma que a preferência foi enfileirada. Consulte `/test-status` até `sms_consent_pending: false` e confirme `sms_consent` / `sms_phone_valid` antes de habilitar o botão SMS. Ao retirar a autorização, desabilite o botão imediatamente. Limite de dez alterações por contato por hora; repetições idempotentes não contam novamente.

Essa operação preserva o histórico, a reserva e o formulário; não manda uma nova abertura e não envia SMS. O disparo manual continua em `POST {WEBHOOK}/reminders/sms`, com sua própria chave idempotente. O automático permanece em T−5 para contatos autorizados.

## Nova abertura

Três templates pt_BR, com valores vindos do banco e sem geração livre:

- Sem reunião: apresenta a Bia e convida a escolher um horário.
- Reunião + objetivo informado: confirma data/hora/fuso/duração e aproveita o objetivo, sem repetir a pergunta.
- Reunião sem objetivo: confirma os dados e pergunta uma única vez o que mais consome tempo no negócio.

Os textos estão em `deploy/form-contact-template.json`, `deploy/booked-contact-template.json` e `deploy/booked-question-template.json`. A configuração usa `TWILIO_FORM_CONTACT_CONTENT_SID`, `TWILIO_BOOKED_CONTACT_CONTENT_SID` e `TWILIO_BOOKED_QUESTION_CONTENT_SID`.

Não há link fictício, promessa de reunião em grupo nem depoimento inventado. A duração é a da reserva (45 minutos na grade atual). Os lembretes T−30/T−15/T−5 são independentes.

Fora da janela do WhatsApp, o backend verifica aprovação e envia `ContentSid` + `ContentVariables`, sem `Body`. Enquanto a aprovação está pendente, aguarda sem forçar envio nem interromper a conversa. [Contrato de variáveis da Twilio](https://www.twilio.com/docs/content/using-variables-with-content-api).

## Preparação 15 minutos após a abertura

Com `BIA_MEETING_PREPARATION_ENABLED=true` e `TWILIO_PREPARATION_CONTENT_SID` aprovado, o worker de follow-up agenda uma mensagem 15 minutos após o envio bem-sucedido da abertura, desde que exista uma reunião futura. A mensagem pede para separar um exemplo de tarefa que consome tempo; não contém depoimento, link ou nova sequência de qualificação. Texto em `deploy/preparation-template.json`.

O envio respeita o consentimento, pausas, atendimento humano e horário de contato. Cancelamento ou remarcação invalida o trabalho antigo. A identidade persistida evita repetição após reinício. A janela de envio é de até cinco minutos após o vencimento e termina antes dos 30 minutos finais da reunião: não existe disparo atrasado nem sobreposição com o lembrete de áudio. Cadastros antigos cuja janela já passou não recebem uma mensagem retroativa.
