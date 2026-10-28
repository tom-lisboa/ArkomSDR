# Formulário com contato automático

Número de teste: **+55 11 93624-6321**. A restrição a um único destinatário foi retirada para atendimento iniciado no WhatsApp. Texto, áudios, imagens e agrupamento de mensagens usam os workers existentes. Follow-ups automáticos permanecem desligados nesta fase. Ligações continuam restritas a lembretes com reunião válida e autorização nativa.

## URL e método

`POST https://SEU_HOST/webhooks/test-form/SUA_CHAVE_DE_TESTE`

Este endereço tem uma chave própria, limitada ao formulário e aos seus disparos de demonstração. Não use a chave OpenAI, credenciais Twilio ou token administrativo na página. Não é necessário cabeçalho Authorization.

Os botões para antecipar o áudio e o convite de ligação usam essa mesma base:
[contrato e exemplo JavaScript](demo-triggers.md). Guarde `submission_id` da resposta para essas ações.

CORS habilitado para a página de teste, inclusive localhost. `OPTIONS` retorna 204. Envie `Content-Type: application/json` e `Idempotency-Key` com UUID. Reutilize o mesmo UUID e corpo ao repetir uma requisição que falhou por rede. Gere outro UUID para uma nova submissão ou alteração de dados.

```json
{
  "name": "Ana",
  "company": "Empresa de teste",
  "phone": "+5511999999999",
  "revenue": 0,
  "goal": "Quero organizar meu atendimento",
  "timezone": "America/Cuiaba",
  "consent": true
}
```

- `name`: nome da pessoa, obrigatório, até 120 bytes.
- `company`: nome da empresa, opcional, até 200 bytes.
- `phone`: WhatsApp da própria pessoa, com país e DDD; aceita espaços, parênteses e hífen. Obrigatório.
- `revenue`: número inteiro não negativo ou `null` se não informado. Zero não desqualifica o contato. Não envie `"R$ 10 mil"` nem uma faixa como se fosse valor exato.
- `goal`: objetivo, opcional, até 2000 bytes.
- `email`: opcional, até 254 bytes.
- `timezone`: fuso IANA; padrão America/Cuiaba.
- `consent`: obrigatório `true`, vindo de um checkbox real: “Autorizo a Arkom a entrar em contato comigo pelo WhatsApp sobre o diagnóstico que estou solicitando.”
- `sms_consent`: opcional, padrão `false`. Usar um checkbox separado: “Quero receber por SMS o lembrete da minha reunião com a Arkom.” Com `true`, informar o celular completo com o nono dígito. Veja [SMS em T−5](sms-reminders.md).

Não envie `data_reuniao`, `owner`, IDs de reserva ou campos de ferramentas neste webhook. A data da reunião continua vindo da agenda e não é criada pela submissão do formulário.

## Fluxo na página

1. Envie o formulário e aguarde HTTP 202.
2. Exiba `message` da resposta. Com `requires_whatsapp_action=false`, a Bia inicia o contato automaticamente.
3. `whatsapp_url` permanece como atalho opcional; abrir o WhatsApp e enviar o código não é mais uma etapa obrigatória.
4. Os dados ficam no PostgreSQL e entram no contexto da IA quando a pessoa responde. Nome, empresa, faturamento e objetivo não precisam ser perguntados novamente.

HTTP 202 significa aceito na fila, não mensagem já entregue. Concluir o formulário não agenda uma reunião nem inicia uma ligação. A primeira mensagem não contém faturamento, objetivo ou outros dados privados.

```js
const WEBHOOK = "https://SEU_HOST/webhooks/test-form/SUA_CHAVE_DE_TESTE";

// Guarde submissionId enquanto houver retry da mesma submissão.
async function enviarFormulario(dados, submissionId) {
  const resposta = await fetch(WEBHOOK, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": submissionId
    },
    body: JSON.stringify(dados)
  });
  const resultado = await resposta.json();
  if (!resposta.ok) throw new Error(resultado.error || "Falha ao enviar");
  return resultado;
}

// No submit: const submissionId = crypto.randomUUID();
// const resultado = await enviarFormulario(dadosDoFormulario, submissionId);
// Mostre resultado.message. O link para o WhatsApp agora é opcional.
// Não tente abrir popup automaticamente depois do await; prefira um botão/link.
```

Resposta HTTP 202:

```json
{
  "status": "contact_queued",
  "requires_whatsapp_action": false,
  "event_id": "form-contact-UUID enviado",
  "submission_id": "UUID enviado",
  "duplicate": false,
  "whatsapp_url": "https://wa.me/5511936246321?text=...",
  "message": "Formulário recebido. A Bia vai iniciar o contato no seu WhatsApp; você não precisa enviar uma mensagem primeiro."
}
```

## Envio pela Twilio

`BIA_FORM_AUTO_CONTACT=true` ativa o contato automático no mesmo webhook. Quando desligado, o vínculo antigo por código continua disponível por compatibilidade. Shadow retorna `queued_for_simulation` e não envia mensagens reais.

A primeira mensagem usa o cadastro e a reserva atual: se já houver reunião, confirma data, hora, fuso e duração. Aproveita o objetivo informado; só pergunta sobre a necessidade quando esse dado não existe. Sem reunião, apresenta a Bia e convida a escolher um horário. Não inclui link de sala.

Dentro da janela de atendimento, sai como texto. Fora dela, o worker seleciona um dos três templates de abertura configurados. Os templates precisam estar aprovados pela Meta; a submissão do formulário **não abre nem altera a janela do WhatsApp**. A margem usada pelo código é de 23 horas desde a última mensagem recebida. Veja os [textos e variáveis da abertura](test-form-troubleshooting.md#nova-abertura).

O worker consulta a aprovação pela API antes de cada envio fora da janela. Enquanto estiver pendente, mantém o mesmo job aguardando, sem consumir tentativas nem transferir o atendimento a um humano. A mensagem expira após 24 horas se o impedimento persistir. `contact_queued` não garante aprovação nem entrega.

## Duplicidade e controles

- Um evento de contato por submissão, persistido com a ativação em uma transação.
- Retry com o mesmo UUID e conteúdo não reenfileira nem consome limite de formulário.
- Novos UUIDs do mesmo contato têm intervalo mínimo de 15 minutos entre mensagens iniciais enviadas; o perfil ainda recebe os dados novos.
- Opt-out, pausa, atendimento humano e propriedade n8n são preservados.
- Uma resposta após a submissão pode substituir a saudação pendente, seguindo pelo atendimento normal.
- Telefones brasileiros com/sem nono dígito reutilizam conversas Twilio existentes. Mensagens assinadas e permissões de ligação seguem o mesmo lead.
- Limites do formulário: 10 submissões por telefone/hora e 60 totais/hora, além dos limites de mensagens configurados.
- A publicação não dispara mensagens para todos os cadastros antigos.

Códigos: 400 JSON inválido/campos desconhecidos; 404 chave incorreta; 409 UUID em conflito ou submissão expirada; 422 dados/consentimento inválidos; 429 limite; 503 indisponível. O administrador pode consultar `GET /v1/jobs/{event_id}` e o job `fx/{event_id}:0` na API privada autenticada.

Referências: [janela e templates](https://www.twilio.com/docs/whatsapp/key-concepts) e [Content API da Twilio](https://www.twilio.com/docs/content/content-api-resources).

## Agendamento pelo formulário

O formulário aceita `slot_id` opcional para reservar antes do contato automático. Consulte `GET {WEBHOOK}/availability` e envie o ID escolhido na submissão; para submissões já concluídas, use `POST {WEBHOOK}/bookings`. Contrato completo e exemplos em [Agenda no formulário](form-calendar.md).

Diagnóstico de erros, consulta de estado e atualização da autorização de SMS: [integração do formulário](test-form-troubleshooting.md).
