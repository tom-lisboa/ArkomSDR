> Atualização: ligações agora são apenas lembretes de reunião existente. Consulte [contexto e roteiro atuais](reminder-context.md). Trechos abaixo sobre `schedule`, `book_slot` e convite automático descrevem a implementação anterior e não estão habilitados.

O worker confere o resultado final da entrega do pedido de autorização. Se a Twilio retornar [37010](https://www.twilio.com/docs/api/errors/37010), registra que o cliente já concedeu autorização permanente e segue com uma única chamada para a solicitação de lembrete. As próximas solicitações podem reutilizar essa autorização; a revogação continua bloqueando chamadas. Mensagem entregue ou lida, sozinha, não autoriza ligação. A autorização permanente não expira após sete dias; a temporária, sim.

Limpar os cadastros no Postgres e os buffers no Redis reinicia o atendimento local. Isso não revoga a autorização armazenada no WhatsApp, que pode continuar dispensando o botão no próximo teste.

# Worker de voz: WhatsApp com autorização

O sexto worker usa **Twilio WhatsApp Business Calling**, **Twilio Media Streams** e **OpenAI Realtime pelo SDK Go v3**. A chamada acontece dentro do WhatsApp. Não há fallback automático para telefonia comum. Integração publicada e primeira chamada real concluída em 28/10/2026, com conversa confirmada pelo usuário. Operação restrita ao número de teste; reserva real ainda não homologada. Veja [resultado e limitações](whatsapp.md).

## Caminho implementado

1. A API autenticada recebe uma intenção `schedule`, `confirm` ou `recover`. Opcionalmente, o formulário publica a intenção `schedule` quando `BIA_VOICE_AUTO_REQUEST=true`.
2. Voz verifica consentimento de contato, dono do lead, pausa, atendimento humano, horário local e entrada pendente. `confirm` exige reunião futura; `recover` exige ausência explicitamente registrada.
3. Envia o template de permissão nativa `VOICE_CALL_REQUEST`. Mensagem comum com botão “sim” não substitui essa permissão. Não há ligação enquanto o cliente não aceitar.
4. O webhook assinado informa `ACCEPTED` ou `REJECTED`. O servidor consulta a mensagem na Twilio para conferir remetente, destinatário, conteúdo e horário; isso impede que a entrega tardia de uma aprovação antiga autorize uma solicitação nova. Eventos repetidos são ignorados.
5. Uma aprovação habilita **uma chamada automática por solicitação**, mesmo quando o cliente permite ligações permanentemente no WhatsApp. O convite local expira em 24 horas. O limite local de solicitações é uma por 24 horas e duas por sete dias, por lead/remetente/modo.
6. A Twilio abre `<Connect><Stream>` em WSS. O worker encaminha áudio μ-law 8 kHz entre a Twilio e uma sessão Realtime, com detecção de fala e interrupção. O limite é cinco minutos.
7. A IA consulta vagas pela ferramenta `list_slots`. A ferramenta `book_slot` exige evidência da última fala transcrita e publica uma ordem durável para **scheduling**. Apenas esse worker reserva ou remarca. A ferramenta aguarda até 12 segundos pelo resultado e distingue `booked` de `pending`, erro ou revisão.
8. Voz publica as transcrições para **context**. A fala da Bia só é registrada como entregue após confirmação de reprodução do áudio completo. Áudio interrompido não vira uma fala completa supostamente ouvida. O contexto mantém o histórico canônico; o ID da sessão Realtime fica separado do `previous_response_id` usado nas Runs de texto.

A chamada usa perfil, reserva atual e até 12 mensagens / 16.000 bytes de histórico. A IA deve se apresentar como assistente de IA. Não se configura gravação da ligação. As transcrições ficam em Postgres; o projeto ainda não implementa política automática de retenção.

## Configuração

Edite o arquivo existente, sem substituir a chave que você já colocou:

```sh
micro /Users/tomlisboa/Desktop/Arkon/.env
```

| Variável | Valor esperado |
|---|---|
| `OPENAI_API_KEY` | Chave já cadastrada localmente |
| `OPENAI_MODEL` | Modelo de texto disponível na sua conta, para execução |
| `OPENAI_REALTIME_MODEL` | Modelo Realtime disponível na sua conta |
| `OPENAI_REALTIME_TRANSCRIPTION_MODEL` | Modelo de transcrição compatível com Realtime |
| `OPENAI_REALTIME_VOICE` | Voz; padrão `marin` |
| `TWILIO_ACCOUNT_SID` | SID `AC…` da conta |
| `TWILIO_AUTH_TOKEN` | Token usado na REST API e nas assinaturas dos webhooks |
| `TWILIO_WHATSAPP_FROM` | Número do remetente em E.164, como `+5565…`, sem `whatsapp:` |
| `TWILIO_CALL_PERMISSION_CONTENT_SID` | Content SID `HX…` do template de autorização nativa |
| `BIA_VOICE_PUBLIC_URL` | Origem HTTPS pública, sem caminho, apontando para o worker de voz |
| `BIA_VOICE_ADDR` | Escuta local; padrão `127.0.0.1:8081` |
| `BIA_VOICE_ENABLED` | `false` por padrão; `true` exige modo live e configuração completa |
| `BIA_VOICE_AUTO_REQUEST` | `false` por padrão; habilita intenção de autorização no cadastro |
| `BIA_VOICE_APPROVED_OUTSIDE_SESSION` | Só `true` após confirmar aprovação do template para envio fora da janela de atendimento |

A chave OpenAI não habilita a Twilio. O remetente precisa estar registrado e habilitado para WhatsApp Calling. Com `BIA_MESSAGING_PROVIDER=twilio`, o serviço usa envio direto e agenda local, sem exigir um gateway externo nem inventar uma URL de agendamento. Contatos precisam de `owner=go`. O webhook do remetente de teste foi configurado; o n8n permanece inalterado.

A janela de mensagens é baseada na última mensagem recebida do lead: preencher formulário não abre por si só uma sessão de WhatsApp. Sem sessão recente e sem template aprovado configurado, a intenção termina com `approved_template_required_outside_session`, sem envio externo. A permissão nativa da plataforma e o consentimento de contato do formulário são verificações distintas.

## HTTP e roteamento

O gateway/roteador de entrada deve encaminhar o webhook de permissão ao serviço de voz preservando todos os campos. A assinatura deve corresponder à URL pública configurada como destino na Twilio; redirecionar um webhook assinado para outra URL não mantém sua assinatura válida. Essa integração está implementada em `/voice/messages`, que valida a URL original e encaminha internamente cada tipo de evento.

| Endpoint público do worker de voz | Função |
|---|---|
| `POST /voice/permission` | Evento nativo de autorização, com assinatura Twilio |
| `POST /voice/twiml/{request_id}` | Instruções de áudio, URL fornecida pela criação da chamada |
| `POST /voice/status/{request_id}` | Estado da chamada; callback fora de ordem não regride estado |
| `GET /voice/stream` | Upgrade WebSocket, assinatura e ticket vinculado à chamada, uso único |
| `GET /healthz` | Saúde do processo |

Esses endpoints usam `X-Twilio-Signature`, não o token da API interna. O proxy deve suportar WSS. O callback de mensagens está em `/voice/messages`; o endpoint dedicado `/voice/permission` não é um gateway geral de WhatsApp.

Solicitação pela API interna, com um `lead_id` existente:

```sh
curl -X POST "http://127.0.0.1:8080/v1/leads/$LEAD_ID/voice" \
  -H "Authorization: Bearer $BIA_API_TOKEN" \
  -H 'Idempotency-Key: voice-demo-001' \
  -H 'Content-Type: application/json' \
  --data '{"purpose":"schedule"}'

curl 'http://127.0.0.1:8080/v1/voice/voice-demo-001' \
  -H "Authorization: Bearer $BIA_API_TOKEN"
```

HTTP 202 confirma a intenção persistida, não autorização nem chamada. Em shadow, o worker grava `simulated` sem acessar provedores.

```sh
cd /Users/tomlisboa/Desktop/Arkon
set -a
source .env
set +a
go build -o bin/bia ./cmd/bia
./bin/bia -migrate
./bin/bia -role voice
```

Os outros cinco workers e a API devem rodar separadamente, ou `./bin/bia -role all` inicia todos para desenvolvimento. A migração 4 adiciona `voice_requests`, `voice_webhooks` e `voice_transcripts` no schema próprio `bia_go`.

## Falhas e limites atuais

- Mutação externa com resultado incerto ou lease perdido fica `uncertain`, sem tentativa automática de nova ligação. Confira a chamada/mensagem na Twilio antes de resolver esse registro operacionalmente; não existe ainda painel ou comando de reconciliação.
- Mensagens automáticas e Runs de texto aguardam enquanto há chamada ativa ou incerta. Opt-out continua sendo processado. Revogação, pausa ou atendimento humano interrompem a sessão na próxima verificação, feita a cada segundo.
- Callbacks ausentes podem deixar o estado ativo após o encerramento remoto. O limite de tempo é enviado à Twilio, mas a consulta automática para reconciliar esses estados ainda não foi implementada.
- Só há uma reserva/remarcação por chamada. Cancelamento e casos não suportados pedem atendimento humano. O worker follow-up não passa a ligar em toda ausência: a intenção `recover` deve ser solicitada explicitamente pela API.
- A validação atual de horário exige na transcrição o ID da vaga ou o horário em `HH:MM`/`HHhMM`. Horários transcritos inteiramente por extenso podem exigir nova confirmação; qualidade de transcrição, sotaques e latência precisam de avaliação real antes de produção.
- O prompt orienta a IA a só anunciar reserva após sucesso da ferramenta. A geração de áudio é livre; não há garantia determinística sobre cada frase falada. Isso exige avaliação de conversas reais antes da ativação.
- Redis continua destinado a agrupar mensagens de WhatsApp. Áudio em tempo real trafega por WebSocket; não passa pelo buffer de fragmentos.

## Referências oficiais

- [Twilio WhatsApp Business Calling: autorização e chamadas](https://www.twilio.com/docs/voice/whatsapp-business-calling)
- [Twilio Media Streams: media, mark e clear](https://www.twilio.com/docs/voice/media-streams/websocket-messages)
- [Assinaturas de webhook Twilio](https://www.twilio.com/docs/usage/security)
- [OpenAI Realtime: conversas e ferramentas](https://developers.openai.com/api/docs/guides/realtime-conversations)
- [SDK Go v3](https://pkg.go.dev/github.com/openai/openai-go/v3)
