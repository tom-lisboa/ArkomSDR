# Webhook do formulário de teste

Número de teste: **+55 11 93624-6321**. A restrição a um único destinatário foi retirada para atendimento iniciado no WhatsApp. Texto, áudios, imagens e agrupamento de mensagens usam os workers existentes. Follow-ups automáticos permanecem desligados nesta fase. Ligações continuam restritas a lembretes com reunião válida e autorização nativa.

## URL e método

`POST https://SEU_HOST/webhooks/test-form/SUA_CHAVE_DE_TESTE`

Este endereço tem uma chave própria, limitada ao recebimento do formulário. Não use a chave OpenAI, credenciais Twilio ou token administrativo na página. Não é necessário cabeçalho Authorization.

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
- `consent`: obrigatório `true`, vindo de um checkbox real: “Quero testar o atendimento da Bia pelo WhatsApp e autorizo o uso destes dados no teste.”

Não envie `data_reuniao`, `owner`, IDs de reserva ou campos de ferramentas neste webhook. A data da reunião continua vindo da agenda e não é criada pela submissão do formulário.

## Fluxo na página

1. Envie o formulário e aguarde HTTP 202.
2. Mostre **“Continuar teste no WhatsApp”**, apontando para `whatsapp_url` da resposta.
3. A pessoa abre o link com o WhatsApp do telefone informado e **envia a mensagem pronta**, sem apagar o código `ARKOM-...`.
4. Nosso webhook da Twilio verifica a mensagem; o código é consumido uma única vez e os dados entram no perfil usado pela Bia. A execução seguinte recebe empresa, nome, faturamento e objetivo. A conversa pode continuar normalmente com texto, áudio e imagens.

A submissão sozinha não dispara mensagem nem ligação. O contato pelo WhatsApp vincula o formulário ao número real e abre a sessão de atendimento. Para números brasileiros, o vínculo aceita a variante oficial reportada pelo WhatsApp sem o nono dígito quando aplicável. Não vinculamos apenas por semelhança do número: é necessário o código recebido após enviar o formulário.

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
// Mostre um <a href={resultado.whatsapp_url}>Continuar teste no WhatsApp</a>.
// Não tente abrir popup automaticamente depois do await; prefira um botão/link.
```

Resposta HTTP 202:

```json
{
  "status": "awaiting_whatsapp",
  "submission_id": "UUID enviado",
  "duplicate": false,
  "whatsapp_url": "https://wa.me/5511936246321?text=...",
  "message": "Formulário recebido. Abra o WhatsApp e envie a mensagem pronta usando o número informado. A Bia receberá os dados do formulário."
}
```

Códigos: 400 JSON inválido/campos desconhecidos; 404 endereço incorreto/desabilitado; 409 mesma chave com conteúdo diferente; 422 campos inválidos/consentimento ausente; 429 limite do teste; 503 indisponibilidade temporária. Limites de formulário: 10 submissões por telefone/hora e 60 no total/hora. Repetição idêntica não consome outra submissão. Link válido por 24 horas desde a primeira submissão; códigos expirados ou enviados por outro número não vinculam dados. Para repetir a vinculação após consumir o código, faça uma nova submissão com novo UUID.

Publicado e verificado no release `r-3ed6c616d356`, schema 8. Limites de mensagens nesta fase: 100 por contato/dia e 300 no total/hora. Testes automatizados com PostgreSQL e detector de corrida passaram. O endpoint público respondeu 202 e o preflight 204, com duplicidade e validação conferidas. Execução sintética na OpenAI passou com a chave atual. O teste de conversa real iniciado por esta página ainda depende do envio pelo aparelho de vocês.
