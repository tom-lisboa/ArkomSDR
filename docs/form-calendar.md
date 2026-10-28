# Agenda no formulário de teste

Use `WEBHOOK` como a URL completa do formulário já configurada na página, incluindo sua chave. As rotas abaixo ficam no mesmo servidor Go. Não use a chave administrativa `BIA_API_TOKEN` no navegador.

## 1. Consultar vagas

```http
GET {WEBHOOK}/availability?timezone=America%2FCuiaba
```

Sem `date`, retorna as vagas dos próximos 14 dias de calendário, começando hoje no fuso informado. Para carregar um dia específico:

```http
GET {WEBHOOK}/availability?date=2026-10-30&timezone=America%2FCuiaba
```

A data é apenas exemplo. `timezone` é opcional e usa o fuso configurado se omitido. O servidor retorna somente vagas livres, com antecedência superior a 30 minutos; não expõe dados de outros contatos, responsáveis ou links de reunião. Horários em RFC3339 incluem o deslocamento do fuso.

```json
{
  "timezone": "America/Cuiaba",
  "minimum_notice_minutes": 30,
  "slots": [
    {
      "id": "ID_REAL_RETORNADO",
      "start": "2026-10-30T14:00:00-04:00",
      "end": "2026-10-30T14:45:00-04:00",
      "date": "2026-10-30",
      "label": "sexta-feira, 30/10, às 14h",
      "duration_minutes": 45
    }
  ]
}
```

`slots: []` significa ausência de vagas no período consultado. Falha de consulta retorna 503, sem fingir que a agenda está cheia. A lista não bloqueia a vaga; a disponibilidade é verificada novamente na reserva.

A grade atual é gerada diariamente no Postgres: todos os dias, 8h–12h e 14h–20h, reuniões de 45 minutos. Uma reunião inteira precisa caber no intervalo. A lista retornada pelo endpoint é a fonte de verdade; não gere horários no navegador.

## 2. Reservar ao concluir o formulário (preferido)

O `POST {WEBHOOK}` existente aceita o novo campo opcional `slot_id`. A página pode carregar os horários, deixar o usuário escolher e enviar tudo em uma única conclusão:

```javascript
const WEBHOOK = /* URL completa já configurada na página */;

async function carregarHorarios(date) {
  const query = new URLSearchParams({ timezone: "America/Cuiaba" });
  if (date) query.set("date", date);
  const response = await fetch(`${WEBHOOK}/availability?${query}`);
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || "availability_failed");
  return result.slots;
}

// Gere uma chave por submissão. Guarde chave + corpo até obter uma resposta
// definitiva; em timeout/queda de conexão, repita exatamente os mesmos valores.
const submissionKey = crypto.randomUUID();
const dados = {
  name: "Ana",
  company: "Empresa Exemplo",
  phone: "+5565999999900", // número real informado pela própria pessoa
  revenue: 5000,
  goal: "Melhorar meu atendimento",
  timezone: "America/Cuiaba",
  consent: true,          // autorização de contato WhatsApp do formulário
  sms_consent: true,      // somente se a pessoa marcou a autorização de SMS
  slot_id: horarioSelecionado.id // ID exato recebido de carregarHorarios()
};

const response = await fetch(WEBHOOK, {
  method: "POST",
  headers: { "Content-Type": "application/json", "Idempotency-Key": submissionKey },
  body: JSON.stringify(dados)
});
const result = await response.json();
if (!response.ok) throw new Error(result.error || "submission_failed");
// HTTP 202: contato enfileirado. result.booking já é uma reserva confirmada
// no banco (não uma simples solicitação). Salve também result.submission_id.
mostrarConfirmacao(result.booking);
```

A resposta mantém os campos antigos (`submission_id`, `event_id`, `whatsapp_url`, `duplicate`, `status`). Com `slot_id`, inclui `booking` com `id`, `provider`, `start`, `end` e os metadados reais da reserva. Sem `slot_id`, `booking` é `null` e o atendimento continua buscando o agendamento pelo WhatsApp. Omitir `slot_id` não apaga uma reserva que o contato já possua.

A ativação do contato, a reserva e o evento de agenda são confirmados na mesma transação. Se a reserva falhar, não há novo contato enfileirado nem reserva parcial; o registro técnico da submissão fica salvo para idempotência. Quando o usuário escolher OUTRO horário após uma rejeição definitiva, envie o novo corpo com uma NOVA chave. Não mude o corpo de uma requisição cujo resultado ainda é desconhecido.

## 3. Reservar após um formulário já concluído

```http
POST {WEBHOOK}/bookings
Content-Type: application/json
Idempotency-Key: UUID_DA_OPERACAO_DE_RESERVA
```

```json
{
  "phone": "+5565999999900",
  "submission_id": "UUID_RETORNADO_PELO_FORMULARIO",
  "slot_id": "ID_REAL_RETORNADO_PELA_DISPONIBILIDADE"
}
```

HTTP 201 cria a reserva; HTTP 200 com `duplicate: true` confirma uma repetição da mesma operação. Ambos retornam `status: "booked"`, `submission_id` e `booking`.

A submissão deve ter sido ativada, ter menos de 24 horas e pertencer ao mesmo telefone. As grafias brasileiras com/sem nono dígito reconhecidas pelo WhatsApp mantêm o mesmo contato. Uma reserva existente não é remarcada silenciosamente por esse endpoint. Remarcação/cancelamento continuam pelo atendimento existente.

CORS e `OPTIONS` estão habilitados para as duas rotas, com `Content-Type` e `Idempotency-Key`. As rotas de reserva operam em modo live e calendário local; shadow não ocupa vagas reais.

## 4. Tratamento de erros na página

| HTTP / erro | Ação da página |
|---|---|
| 409 `slot_no_longer_available` | Recarregar vagas e pedir nova escolha; usar nova chave para a escolha diferente. |
| 409 `lead_already_booked` | Informar que já existe reserva; encaminhar ao WhatsApp para consultar ou remarcar. |
| 409 `contact_busy` | Bia está processando uma interação. Aguardar alguns segundos e repetir a mesma requisição/chave. |
| 409 `booking_in_progress` | Há uma operação de agenda pendente. Aguardar sua conclusão; não criar outra seleção automaticamente. |
| 409 `idempotency_conflict` | A mesma chave foi usada com dados diferentes. Corrigir o estado do formulário; não repetir em loop. |
| 409 `test_submission_expired` | Criar uma nova submissão do formulário. |
| 409 `form_activation_required` | Submissão ainda não foi ativada para contato automático. |
| 409 `contact_not_eligible` | Contato pausado, opt-out, atendimento humano ou fora da operação Go; não tentar contornar. |
| 404 `test_submission_not_found` | Conferir `submission_id` e telefone. |
| 404 `slot_not_found` | Recarregar disponibilidade. |
| 422 | Corrigir os campos indicados no erro. |
| 503 | Mostrar indisponibilidade temporária e permitir repetir a mesma requisição/chave. |

## 5. Como a Bia usa a reserva

O worker de agenda aplica o evento confirmado antes dos próximos efeitos de atendimento. O mesmo `booking` alimenta o contexto de texto, o contexto da ligação e os lembretes. A reserva dispara as sequências habilitadas, incluindo SMS em T−5 com consentimento; áudio e ligação continuam dependendo das respectivas configurações.

O contexto de cada Run informa `meeting_status: upcoming` ou `not_scheduled`. Com reunião futura marcada, a Bia reforça data/hora/fuso, responde às dúvidas sobre a reunião e evita nova qualificação ou oferta de agenda. Só apresenta alternativas quando a pessoa pede remarcação. Sem reserva, conduz para o agendamento.

A abertura aprovada continua exatamente `Oi, tudo bem?\n\nRetomando nossa conversa por aqui`. Fora da janela do WhatsApp, ela é enviada pelo template aprovado. A confirmação detalhada aguarda a pessoa responder para abrir a janela; a reserva já fica confirmada no formulário e no banco durante essa espera. Não prometemos link de sala, pois nenhum está configurado.

## Validação

Testes de integração cobrem disputa pela mesma vaga, repetição idempotente, vínculo telefone/submissão, lease de atendimento ativo, rollback de ativação, reserva visível ao worker, criação dos lembretes e confirmação aguardando janela sem chamar o provedor. Os testes locais usam Postgres isolado e transportes simulados, sem mensagens para clientes.
