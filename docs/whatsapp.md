# Integração direta do WhatsApp

Publicada em 28/10/2026, release `r-c912a41f4a60`. O remetente Twilio está `ONLINE` e utiliza `POST https://24.199.100.186/voice/messages` para mensagens e respostas ao botão nativo de autorização de ligação.

## Fluxo

1. O servidor verifica a assinatura na URL original, conta, destinatário e restrição de teste. Requisições sem assinatura recebem 403.
2. Eventos `VOICE_CALL_REQUEST` seguem para a autorização de voz. Não passam pelo agente de texto e não viram intenções de cancelar reuniões.
3. Mensagens comuns são conferidas na API Twilio. O horário original é usado para idempotência e janela de atendimento; reenvios antigos não abrem uma nova janela.
4. O primeiro contato iniciado pelo usuário cria o perfil de atendimento. Perfis existentes preservam opt-out, proprietário e controle humano. O ID da Twilio deduplica a entrada.
5. Contexto recebe o evento no PostgreSQL e agrupa os fragmentos usando Redis. Execução escolhe o agente e usa Responses com o histórico e `previous_response_id`.
6. O envio direto reserva uma tentativa em `bia_go.twilio_messages` antes do POST. Um reprocessamento com SID apenas consulta o status; uma tentativa interrompida sem SID exige revisão. Não existe reenvio cego.
7. O sistema aguarda `sent`, `delivered` ou `read` por até 15 segundos antes de considerar a mensagem enviada. `queued` não é sucesso. Resultado incerto bloqueia automação e pede revisão. `sent` confirma envio pelo provedor, não leitura pelo destinatário.

## Teste controlado

- `BIA_MODE=live`, `BIA_MESSAGING_PROVIDER=twilio`.
- `BIA_TEST_PHONE` limita todos os envios e chamadas ao número expressamente autorizado nesta conversa. Outras entradas são reconhecidas sem automação.
- A exceção de horário só vale para esse número, porque o usuário pediu o teste imediato. Follow-ups e lembretes programados ficam desativados durante o teste.
- `BIA_VOICE_AUTO_REQUEST=false`: a intenção da chamada é solicitada pela API interna.
- `BIA_VOICE_APPROVED_OUTSIDE_SESSION=false`: é necessária uma mensagem recente do usuário para enviar o botão sem template aprovado fora da janela.
- Voz: `gpt-realtime`, `marin`, transcrição `gpt-4o-transcribe`. Pré-teste real gerou áudio μ-law; ver resultado (`realtime-smoke-result.json`, evidência local não versionada).
- A aplicação TwiML continua em `/voice/incoming`, encerrando chamadas recebidas. A chamada de saída usa as rotas específicas da solicitação.

A autorização neste chat permite preparar e executar o teste, mas a chamada de WhatsApp só começa após aceitar a permissão nativa no próprio WhatsApp.

## Limites atuais

- Envio de texto funciona dentro da janela de atendimento de 23 horas adotada pelo adaptador. Fora dela, o envio é bloqueado até integrar templates aprovados para cada finalidade. Cadastro no site não abre essa janela.
- Áudio e imagem usam download autenticado e leitura no worker de mídia. O contexto reúne mensagens e aguarda todos os anexos antes de executar. Veja [janelas, limites e digitação](whatsapp-media.md). A mídia em tempo real da ligação continua usando outra conexão.
- Agenda local sem vagas publicadas não gera horários fictícios nem links de agendamento. A IA pode pedir preferência, mas o teste de ligação não comprova reserva real.
- Tentativas ambíguas e callbacks ausentes ainda exigem reconciliação operacional; não há painel de suporte ou reenvio automático.
- A criação de perfil a partir de mensagem recebida serve ao atendimento solicitado. Não equivale a consentimento para campanhas externas.

## Verificações

`go test -race ./...` passou com PostgreSQL 17 isolado, incluindo assinatura, replay, preservação de opt-out/proprietário, limite de destinatário, janela expirada e interrupção de envio. `go vet ./...` e compilação Linux passaram. A migração 5 adiciona o registro durável dos envios Twilio.

O resultado de uma chamada real deve ser registrado após autorização e execução; o pré-teste de áudio não substitui esse resultado.

## Resultado real em 28/10/2026

- Mensagem “teste” recebida, processada pelo agente e respondida; a Twilio confirmou leitura.
- Botão nativo entregue e lido. A Twilio devolveu SID `MM`, corrigido no adaptador para aceitar `SM` e `MM` com validação hexadecimal.
- O primeiro aceite chegou enquanto a solicitação estava `uncertain` pelo erro de SID. A operação foi reconciliada após confirmar o envio na API, sem reenviar o botão. O usuário aceitou novamente; nenhuma autorização foi fabricada.
- Chamada concluída às 09:57:39 de Cuiabá, duração de 14 segundos. WebSocket Twilio e sessão OpenAI conectados; uma fala da Bia com reprodução integral confirmada e quatro falas do usuário transcritas. Esses contadores não representam necessariamente todos os fragmentos de áudio ou falas interrompidas.
- O usuário confirmou: “Ouvi a Bia e conversamos”. Não foi realizada reserva real.
- O endereço WhatsApp confirmado pela mensagem recebida omite o nono dígito do telefone informado. A configuração foi ajustada somente para esse endereço confirmado; não há transformação indiscriminada de telefones.

A solicitação terminou em `completed`; não há nova chamada automática programada. Evidência técnica (`voice-live-test-result.json`, evidência local não versionada).
