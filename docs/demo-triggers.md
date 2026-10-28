# Botões de demonstração: áudio, ligação e SMS

Use a **mesma URL do webhook já configurada no formulário**, acrescentando:

| Botão | Método | Sufixo |
|---|---|---|
| Enviar áudio agora | POST | `/reminders/audio` |
| Lembrar por ligação agora | POST | `/reminders/call` |
| Enviar SMS agora | POST | `/reminders/sms` |

Exemplo de base: `https://24.199.100.186/webhooks/test-form/SUA_CHAVE_DE_TESTE`.
Não use credenciais OpenAI/Twilio nem o Bearer administrativo na página.
Os endpoints têm CORS e usam a mesma chave limitada à demonstração.

## Corpo dos POSTs

```json
{
  "phone": "+5511999999999",
  "submission_id": "UUID retornado no envio do formulario"
}
```

Cabeçalhos: `Content-Type: application/json` e `Idempotency-Key: UUID_DO_CLIQUE`.
Cada ação tem seu próprio UUID. Guarde-o para repetir a mesma requisição após
falha de rede. O mesmo UUID/corpo nunca dispara uma segunda vez, mesmo depois de
reiniciar o servidor. Outro número, submissão ou ação com o mesmo UUID retorna 409.

O número deve corresponder à submissão ativada do formulário, válida por 24h.
A pessoa precisa ter respondido no WhatsApp nas últimas 23h para áudio/ligação e ter uma reunião
futura no nosso banco. Os botões usam essa reserva; não criam uma nova data,
não antecipam a reunião e não ligam a cadência automática.

SMS exige `sms_consent: true` na submissão, mas não depende da janela do WhatsApp.
Use `criarDisparo("sms", dados)` no exemplo abaixo. [Detalhes do SMS](sms-reminders.md).

## O que acontece

- **Áudio:** API → fila followup → worker de mídia → gravação no WhatsApp.
- **Ligação:** API → followup envia o texto da reunião → confirma envio →
  worker de voz envia o botão nativo → aguarda aceite → consulta o worker da
  agenda → faz uma ligação de lembrete com IA.
- **SMS:** API → worker de follow-up → Twilio SMS, com autorização SMS registrada no formulário.

A sequência manual começa assim que os workers a processam, sem esperar T−30/T−15/T−5
ou o horário da cadência. Mensagens do cliente ainda sendo agrupadas e uma ligação
em andamento podem adiar o processamento. A ação expira em 10 minutos ou no início
da reunião, o que ocorrer primeiro. Para a ligação, o aceite precisa chegar nesse prazo.

Pausa, opt-out, atendimento humano, troca/cancelamento da reunião e restrições de
envio são conferidos novamente durante a execução. O clique na página não substitui
o aceite do destinatário no WhatsApp. As mesmas regras de solicitação de permissão
do worker de voz continuam valendo: no máximo uma por 24h e duas por sete dias.

O áudio definitivo ainda precisa ser instalado seguindo [followups.md](followups.md).
Até isso ocorrer, o endpoint de áudio retorna 503 `reminder_audio_not_ready` e não
enfileira um envio falso. O botão de ligação funciona independentemente desse arquivo.

## Exemplo para ligar aos botões

```js
// Reaproveite a variável que já aponta para o webhook do formulário.
// const WEBHOOK = "https://24.199.100.186/webhooks/test-form/SUA_CHAVE_DE_TESTE";

function criarDisparo(acao, dados) {
  const requestId = crypto.randomUUID(); // um UUID por ação intencional
  const body = JSON.stringify({
    phone: dados.phone,
    submission_id: dados.submission_id
  });
  let emAndamento = null;

  return function disparar() {
    if (emAndamento) return emAndamento; // clique duplo usa a mesma requisição
    emAndamento = (async () => {
      const r = await fetch(`${WEBHOOK.replace(/\/$/, "")}/reminders/${acao}`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "Idempotency-Key": requestId },
        body
      });
      const result = await r.json();
      if (!r.ok) throw new Error(result.error || "Falha ao acionar lembrete");
      return result;
    })().finally(() => { emAndamento = null; });
    return emAndamento;
  };
}

// Crie uma vez após receber o resultado do formulário, não a cada clique.
const dados = { phone: dadosDoFormulario.phone, submission_id: resultado.submission_id };
const enviarAudio = criarDisparo("audio", dados);
const solicitarLigacao = criarDisparo("call", dados);
const enviarSMS = criarDisparo("sms", dados);
// onClick do primeiro botão: await enviarAudio()
// onClick do segundo botão: await solicitarLigacao()
// onClick do terceiro botão: await enviarSMS()
// Desabilite o botão enquanto aguarda e mostre erros na própria página.
// Para uma NOVA demonstração, crie outro disparo explicitamente (respeitando os limites).
```

Para um botão HTML de SMS, crie-o com `type="button"` para não reenviar o formulário:

```html
<button id="enviar-sms" type="button">Enviar SMS agora</button>
<p id="sms-status" role="status"></p>
```

Depois de criar `enviarSMS` no código acima, conecte o clique:

```js
const botaoSMS = document.querySelector("#enviar-sms");
const statusSMS = document.querySelector("#sms-status");
botaoSMS.addEventListener("click", async () => {
  botaoSMS.disabled = true;
  try {
    const disparo = await enviarSMS();
    statusSMS.textContent = disparo.duplicate
      ? "Esta solicitação já foi recebida. Consulte o progresso."
      : "SMS solicitado. Aguardando o processamento.";
    // Consulte disparo.status_url conforme o exemplo abaixo.
  } catch (erro) {
    statusSMS.textContent = erro.message;
  } finally {
    botaoSMS.disabled = false;
  }
});
```

O contato precisa ter uma reunião futura e ter marcado `sms_consent: true` no
formulário. O botão antecipa o lembrete, mas não altera a data da reunião. Nenhuma
autorização de chamada ou janela aberta no WhatsApp é necessária para SMS.

Resposta HTTP 202:

```json
{
  "status": "queued",
  "action": "call",
  "request_id": "UUID_DO_CLIQUE",
  "job_id": "fx/demo:UUID_DO_CLIQUE",
  "duplicate": false,
  "requires_call_permission": true,
  "status_url": "/webhooks/test-form/SUA_CHAVE_DE_TESTE/reminders/call/UUID_DO_CLIQUE"
}
```

202 significa **enfileirado**, não áudio entregue nem chamada já realizada.
Em shadow, o status é `queued_for_simulation`; nenhuma mensagem real é enviada.

## Mostrar progresso

Faça GET na `status_url` retornada, no host do webhook, a cada 3–5 segundos durante
a demonstração. Não use o host da página Vercel para resolver esse caminho:

```js
const url = new URL(disparo.status_url, WEBHOOK);
const progresso = await fetch(url).then(r => {
  if (!r.ok) throw new Error("Não foi possível consultar o disparo");
  return r.json();
});
// progresso.steps: [{ step, status, reason? }]
```

Etapas: `followup`, `audio`, `permission_request`, `call`.
`followup: done` apenas informa que a etapa inicial terminou. Para áudio, espere
`audio: done` (envio confirmado pelo provedor, não reprodução pelo usuário).
Na ligação: `awaiting_permission` pede que a pessoa aceite no WhatsApp; `ringing`
é chamando; `in-progress` é em andamento; `completed` é encerrada. Estados como
`skipped`, `dead`, `failed`, `uncertain`, `expired`, `rejected`, `busy`, `no-answer`
ou `canceled` encerram a tentativa; mostre `reason` quando existir. Em simulação,
`followup: simulated` encerra a ação sem etapas reais seguintes.
Pare a consulta no estado terminal, após 10 minutos ou ao sair da página.

## Erros que a página deve tratar

| HTTP / erro | Orientação |
|---|---|
| 404 | Conferir a URL/chave e o ID da submissão. |
| 409 `submission_phone_mismatch` | Usar o mesmo telefone do formulário enviado. |
| 409 `test_submission_not_active` | Concluir um novo formulário de teste. |
| 409 `future_booking_required` | Agendar com a Bia antes de demonstrar o lembrete. |
| 409 `whatsapp_reply_required` | Enviar uma mensagem à Bia pelo WhatsApp e aguardar o processamento. |
| 409 `contact_blocked` | Contato pausado, sem consentimento, em atendimento humano ou fora da operação Go. |
| 409 `voice_request_in_progress` | Já há convite/chamada em andamento; concluir essa tentativa. |
| 409 `idempotency_conflict` | Não reutilizar UUID entre ações ou números diferentes. |
| 429 `test_trigger_rate_limit` | Aguardar a ação pendente/intervalo de 60s; máximo global de 60 ações/hora. |
| 429 `permission_request_limit` | O limite de pedidos de ligação desse contato foi atingido. |
| 503 `reminder_audio_not_ready` | Instalar o áudio definitivo. |
| 503 `voice_not_enabled` | Habilitar a integração de voz no servidor. |
| 409 `sms_consent_required` | Autorizar SMS no formulário. |
| 503 `sms_not_enabled` | Configurar o remetente SMS no servidor. |

Nenhum desses erros deve ser mostrado como envio concluído.
