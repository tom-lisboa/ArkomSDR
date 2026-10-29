# SMS cinco minutos antes da reunião

O worker de follow-up envia um lembrete transacional por SMS em **T−5 minutos**,
usando a reserva atual no PostgreSQL e a mesma API Twilio da conta. O canal tem
remetente e registro de entrega próprios; não utiliza o prefixo `whatsapp:`,
template da Meta nem a janela de atendimento do WhatsApp.

O remetente configurado é `+18572902049`, já existente na conta e com capacidade
SMS confirmada pela API. No Brasil, a operadora pode substituir o identificador
exibido por outro número; isso é previsto nas [orientações da Twilio para o Brasil](https://www.twilio.com/en-us/guidelines/br/sms).
A disponibilidade da rota e a entrega no aparelho ainda dependem da Twilio e da
operadora. Se o envio retornar `21408`, habilitar Brasil em Messaging → Settings →
Geo Permissions no Console. A consulta de permissões por API desta conta retornou
404; não consideramos a rota brasileira validada por esse teste.

## Formulário

Acrescente um checkbox opcional, inicialmente desmarcado:

> Quero receber por SMS o lembrete da minha reunião com a Arkom. Posso cancelar os avisos enviando SAIR no WhatsApp da Arkom.

Envie `sms_consent: true` somente quando a pessoa selecionar essa opção:

```json
{
  "name": "Ana",
  "company": "Empresa de teste",
  "phone": "+5511999999999",
  "timezone": "America/Cuiaba",
  "consent": true,
  "sms_consent": true
}
```

O checkbox `consent` existente continua sendo o consentimento de contato no
WhatsApp. Cadastros anteriores não recebem autorização SMS retroativa. O servidor
guarda a versão `test-form-sms-v1` e o telefone original da submissão. Para celulares
brasileiros, informar o número completo, incluindo o nono dígito; não usamos para
SMS uma versão abreviada recebida do WhatsApp. Não envie `sms_phone` ou
`sms_consent_version` diretamente no webhook público: o servidor os define.
Uma nova submissão sem `sms_consent: true` retira essa opção do perfil quando
processada. `SAIR` no WhatsApp mantém o bloqueio geral de novos contatos.

## Cadência e conteúdo

- T−30: áudio no WhatsApp, quando a cadência de áudio/voz estiver ativada.
- T−15: texto, autorização nativa e ligação, quando essa cadência estiver ativada.
- **T−5: SMS**, com ativação independente do arquivo de áudio e da cadência de voz.

Exemplo do texto, com data e horário reais no fuso do contato:

> Arkom: sua reuniao e 28/10 15:00. Confirme presenca pelo WhatsApp +5511936246321. Para parar avisos, envie SAIR por la.

O texto não inclui link de sala, faturamento ou objetivo do formulário. Ele é
montado por código e não depende de uma geração do modelo.

O agendador reconcilia reservas a cada 30 segundos e o consumidor envia os jobs
vencidos. O horário desejado é T−5; execução e entrega têm latência de fila/rede.
Se a reunião for marcada com menos de cinco minutos de antecedência, o lembrete
fica imediatamente elegível. Um reinício dentro desses cinco minutos permite
recuperar o job; após o início da reunião, o envio é descartado. O pedido à Twilio
usa `ValidityPeriod` limitado ao tempo restante, até 300 segundos, para limitar
permanência na fila do provedor. Isso não garante o instante da entrega na operadora.
Veja a [referência Messages da Twilio](https://www.twilio.com/docs/messaging/api/message-resource).

Os lembretes pedidos para reuniões às 08:00 podem sair às 07:55. Não seguem o horário
das retomadas comerciais, pois acompanham a reunião que a pessoa agendou.

## Botão de demonstração

Use a mesma base e contrato dos [outros botões](demo-triggers.md):

`POST {WEBHOOK}/reminders/sms`

```json
{
  "phone": "+5511999999999",
  "submission_id": "ID retornado pelo formulario"
}
```

Cabeçalhos `Content-Type: application/json` e `Idempotency-Key: UUID_DO_CLIQUE`.
Na função JavaScript existente: `criarDisparo("sms", dados)`.
A pessoa precisa ter autorizado SMS no formulário e possuir uma reunião futura.
Não precisa responder no WhatsApp para abrir uma janela. HTTP 202 informa fila;
consulte `status_url`. Para esta ação, `followup: done` significa que a Twilio
informou `sent` ou `delivered`, não que a pessoa leu a mensagem.
`sms_consent_required` retorna 409; `sms_not_enabled`, 503.

## Operação

```dotenv
BIA_SMS_REMINDERS_ENABLED=true
TWILIO_SMS_FROM=+18572902049
```

`BIA_SMS_REMINDERS_ENABLED` controla somente a cadência automática SMS. O remetente
configurado também permite o botão manual. As credenciais são as mesmas variáveis
Twilio já guardadas no servidor. O scheduler pode ficar ligado enquanto
`BIA_MEETING_REMINDERS_ENABLED=false` e `BIA_DISABLE_FOLLOWUPS=true`.

A etapa tem ID estável por contato, reserva e horário. Repetir a reconciliação ou
reiniciar o processo não reenvia o SMS. Reserva, opt-out, pausa, atendimento humano
e consentimento SMS são rechecados no envio. Cancelamento ou remarcação invalida o
job antigo. `bia_go.sms_messages` registra a tentativa antes do POST e guarda o
SID/status; tentativas incertas ficam para revisão e não são repetidas cegamente.
Uma falha de SMS não transfere toda a conversa do WhatsApp para um humano.

O painel privado `GET /v1/followups` expõe `sms_enabled` e `sms_before_minutes: 5`.
Os jobs usam a fila `followup`, propósito `meeting_sms` e tipo `send_sms`.
Em shadow não há envio. Os limites de mensagens também contam os SMS.

Validação local: PostgreSQL isolado, API Twilio simulada, T−5, ausência de sessão
WhatsApp, repetição/reinício, consentimento, remarcação/cancelamento, shadow e falha
ambígua. Não foi enviado SMS real durante a publicação; o primeiro teste no aparelho
deve partir do formulário com autorização e do botão de demonstração.

Diagnóstico de erros, consulta de estado e atualização da autorização de SMS: [integração do formulário](test-form-troubleshooting.md).
