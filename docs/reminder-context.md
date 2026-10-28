# Contexto compartilhado: formulário, texto e ligação

Os dois agentes recebem o mesmo resumo gerado em Go a cada execução:

| Campo do agente | Origem | Quando ausente |
| --- | --- | --- |
| `nome_empresa` | `company` no formulário | string vazia |
| `nome_pessoa` | `name` no formulário | string vazia |
| `faturamento` | `revenue` no formulário, número inteiro | `null` |
| `data_reuniao` | reserva atual, com horário e fuso | `null` |

`revenue: 0` é faturamento zero informado, não ausência de informação nem motivo para ignorar o contato. A unidade e o período são os do formulário de origem; o serviço não presume um período mensal nem converte uma faixa em valor. Data da reunião significa quando ela acontecerá, não quando o formulário foi preenchido. Remarcação e cancelamento atualizam o estado canônico usado no próximo Run. Os campos existentes do formulário, como objetivo, continuam preservados; telefone e e-mail não são incluídos desnecessariamente no prompt.

O formulário continua aceito em `POST /v1/forms`, com bearer token privado e `Idempotency-Key`. Exemplo do corpo (dados fictícios):

```json
{
  "company": "Empresa Exemplo",
  "name": "Ana",
  "phone": "+5565999999900",
  "revenue": 0,
  "goal": "Organizar o atendimento",
  "timezone": "America/Cuiaba",
  "consent": true,
  "consent_text_version": "form-v1",
  "channel": "gateway"
}
```

Não envie data arbitrária como se fosse reserva. O worker de agendamento publica os eventos da reserva confirmada. O formulário não exige site nem tamanho da equipe. Um novo formulário sem `company`/`revenue` preserva os valores já conhecidos. Nada altera o formulário/site externo ou o n8n automaticamente: o produtor precisa enviar esses campos à nossa API.

Consulta privada do resumo: `GET /v1/leads/{lead_id}/context`.

## Ligação de lembrete

`POST /v1/leads/{lead_id}/voice`, corpo `{"purpose":"confirm"}` (padrão quando omitido), com `Idempotency-Key`. Só é aceita para reunião futura existente. `schedule` e `recover` deixaram de ser finalidades executáveis; a entrada de formulário não gera ligação, mesmo com a antiga flag de convite automático. Histórico e registros antigos são preservados.

O pedido fica vinculado ao ID e horário da reserva. Antes da autorização, da discagem e da sessão de áudio, a voz envia uma consulta para `bia_go.agenda_requests`. **O worker de agendamento** responde consultando reserva, horário reservado e perfil no PostgreSQL. Cancelamento, mudança da reserva, opt-out, bloqueio de atendimento ou falta de dados impedem o lembrete. Indisponibilidade do worker não permite inventar reunião. Redis continua responsável pela aceleração do buffer de mensagens, não por ser fonte dos dados comerciais.

Ferramentas da voz:

- `get_meeting`: atualiza os dados daquela reunião; não aceita ID de outro lead.
- `record_reminder_note`: grava confirmação verbal, preferência, pedido de cancelamento ou dúvida em `crm_notes` de forma idempotente. **Não muda a reserva nem envia notificação automática.**
- `end_call`: aguarda fim da resposta e confirmação de reprodução do áudio pela Twilio, com limite de espera.
- `voicemail_detection`: gera uma única resposta curta com data/hora e encerra após reprodução. Não inclui faturamento, empresa ou briefing. A detecção é feita pelo modelo, não por um classificador independente.

As antigas ferramentas de reservar/consultar novas vagas não ficam disponíveis na voz, e o caminho interno antigo de reserva por voz rejeita a operação. O agente de texto mantém seu fluxo próprio de agendamento.

## Variáveis adicionais do roteiro

O roteiro adaptado está em `internal/voiceworker/reminder_prompt.txt`. Não há substituição de conteúdo do formulário por instruções: os valores são enviados em um bloco JSON separado, marcado como dados.

Dados opcionais de uma reserva local podem ser preenchidos por `PUT /v1/leads/{lead_id}/bookings/{booking_id}/briefing` (privado):

```json
{
  "consultor": "Ana",
  "consultor_genero": "ela",
  "formato": "Online",
  "pauta": "Organização do atendimento",
  "contexto": "Cliente quer reduzir trabalho manual",
  "ambiente": "silencioso"
}
```

Nome da pessoa, empresa, faturamento e objetivo vêm do formulário. Data, horário, link e duração vêm da reserva; hoje e fuso são calculados no servidor. Consultor, pauta, briefing e ambiente são opcionais. Não inferimos nome do consultor a partir de seu ID nem gênero a partir do nome. Sem consultor, a Bia usa “time da Arkom”. Sem dados de acesso, não inventa formato/link.

As notas ficam disponíveis à equipe em `GET /v1/leads/{lead_id}/reminder-notes`. Não há notificação automática ao consultor nesta etapa.

## Validação

Testes com PostgreSQL e detector de condições de corrida cobrem: perfil compartilhado entregue aos dois modelos, zero vs. desconhecido, preservação de empresa em atualizações parciais, mudança de dia por fuso, leitura via worker, isolamento de briefing por lead, recusa de ligações sem reserva, bloqueio após cancelamento/remarcação, rejeição de ferramentas antigas, notas idempotentes sem mutação de agenda e desligamento após reprodução da despedida/recado.

Nenhuma ligação real foi disparada para homologar este novo roteiro. Horários, consultores e informações reais do formulário precisam ser cadastrados/integrados para personalização real. A conexão com agenda externa continua fora desta etapa; usamos a agenda local do projeto.

Referência da sequência de ferramentas e respostas Realtime: https://developers.openai.com/api/docs/guides/realtime-conversations
