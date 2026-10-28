# Validação local — 27/10/2026

Executado nesta máquina:

- `go test -race -count=1 ./...` — aprovado, incluindo a suíte de integração com PostgreSQL 17 em banco isolado.
- `go vet ./...` — aprovado.
- `go build -o bin/bia ./cmd/bia` — aprovado.
- `go run ./cmd/replay -input examples/replay.jsonl` — seis eventos sintéticos processados sem rede.
- Binário real: migração, startup, readiness HTTP, envio do formulário, processamento pelos workers, consulta de métricas e encerramento por SIGTERM — aprovado.

Resultado do smoke test em `smoke-result.json`: readiness `ready`, HTTP 202 no formulário, faturamento zero com próximo passo de agenda, duas ações simuladas, zero mensagens reais enviadas e disponibilidade `empty`.

Cobertura relevante: idempotência concorrente, conflito de payload, lease por lead, rejeição de worker antigo, operação interrompida encaminhada para revisão, impossibilidade de transformar efeitos de shadow em envios live, vaga disputada, sobreposição de horários, remarcação transacional, cancelamento idempotente, opt-out, atraso sem cancelamento, lembrete invalidado por remarcação, correção de presença, schema do SDK OpenAI, resposta de recusa e bloqueio de URLs de áudio não autorizadas.

As chamadas da OpenAI e dos provedores foram simuladas em servidores HTTP de teste. Não houve avaliação real da qualidade de conversa de um modelo, envio de WhatsApp, acesso aos bancos antigos ou teste de contratos em produção. O Dockerfile foi criado, mas a imagem não foi construída: o daemon Docker local estava desligado. O Postgres de teste foi iniciado separadamente e encerrado após os testes.

O SHA-256 do JSON original foi comparado ao inventário depois da implementação: arquivo inalterado, 127 nós catalogados.

## Arquitetura de três workers

Após a separação, a suíte inteira foi executada novamente com `-race -count=1`, usando Postgres 17 real em `arkom_bia_test`: aprovada. `go vet ./...` e a compilação do binário atualizado também passaram.

Cobertura acrescentada:

- Fragmentos ordenados, idempotência e uma Run ativa por conversa.
- Ingestão concorrente por quatro consumidores sem perder nenhuma das 20 mensagens.
- Buffer Redis com versões, proteção contra limpeza antiga e prazo de agrupamento (go-redis + miniredis).
- Postgres mantém o processamento quando Redis está ausente.
- Execução publica resultado; somente contexto persiste o head.
- Expiração de head por idade, profundidade e tokens; rejeição de lease expirado.
- `previous_response_id` por agente, `store: true`, instruções reenviadas e fallback sem ID inválido, via servidor HTTP simulado.
- Espera por mídia, texto extraído integrado à janela e timeout explícito.
- Contratos de extração de imagem/PDF pelo SDK, com downloads e respostas simulados.
- Resposta antiga descartada após nova entrada; `sair` em fragmento continua acionando opt-out sem modelo.
- Campo interno `context` rejeitado na validação e conflitos de IDs entre filas rejeitados.

O smoke test workers-smoke-result.json (`workers-smoke-result.json`, evidência local não versionada) iniciou os modos `api`, `context`, `execution` e `media` como quatro processos locais, com dois consumidores por papel de worker. Duas mensagens geraram uma Run; um anexo sem processador configurado gerou revisão humana em uma segunda Run. Ao final: zero Runs ativas, zero tarefas pendentes, zero tarefas mortas, quatro efeitos simulados e zero envios externos. Os quatro processos encerraram normalmente via SIGTERM.

Nesse smoke test Redis estava indisponível: foi exercitada a recuperação pelo Postgres. As operações Redis foram validadas separadamente contra miniredis, não contra um servidor Redis de produção. O cluster Postgres usado nesta rodada foi temporário e encerrado ao concluir. O fluxo n8n original foi novamente conferido pelo SHA-256 e permanece intacto.

## Expansão para cinco workers: agendamento e follow-up

A suíte completa passou novamente com `go test -race -count=1 ./...`, incluindo Postgres 17 real em banco isolado. `go vet ./...` e a compilação de `bin/bia` passaram.

Os novos testes de integração cobrem:

- Reserva, remarcação e cancelamento consumidos exclusivamente por `scheduling`, com agenda local transacional.
- Confirmações na fila de agenda; lembretes, retomadas e recuperação na fila `followup`.
- Cancelamento, opt-out e atendimento humano já enfileirados aplicados antes de um lembrete.
- Ordem dos eventos preservada entre filas, incluindo `cadence.resume`.
- Trava por lead compartilhada entre agenda e follow-up; worker expirado não pode concluir, e efeito interrompido exige reconciliação.
- Adiamento por horário de contato sem gastar tentativas de provedor; descarte ao ultrapassar validade/reunião.
- Recibo de follow-up publicado para contexto e recuperação somente após evento explícito de no-show.
- Invalidação de seis lembretes antigos após remarcação/cancelamento e liberação das vagas.
- Migração de jobs preexistentes, preservando status, e reaplicação idempotente da migração 3.

O teste do executável five-workers-smoke-result.json (`five-workers-smoke-result.json`, evidência local não versionada) rodou a API e os cinco workers em seis processos locais. O evento de reserva permaneceu pendente até iniciar `scheduling`. O lembrete permaneceu pendente até iniciar `followup`. Após cancelamento, dois lembretes obsoletos foram descartados. Um prazo foi antecipado apenas no banco descartável para exercitar a fila sem esperar horas.

Resultado: zero tarefas pendentes, zero tarefas mortas, zero envios reais e encerramento normal dos seis processos. Agendamento e follow-up continuam em `shadow` por padrão. Nenhum serviço antigo foi conectado, nenhum WhatsApp foi enviado e o n8n não foi modificado. O cluster temporário desta rodada foi encerrado e removido após a validação.

## Sexto worker: voz e autorização de chamada

Em 27/10/2026, `go test -race ./...` passou com PostgreSQL 17 real no banco descartável `arkom_bia_test`. `go vet ./...` e a compilação de `bin/bia` também passaram. O SDK OpenAI v3 fornece a conexão Realtime; nos testes de áudio essa conexão é simulada e o WebSocket do lado Twilio roda em servidor HTTP local.

Os testes acrescentados verificam:

- Uma única intenção ativa por lead/modo, idempotência, aprovação necessária e no máximo uma discagem por intenção.
- Aprovação recebida no mesmo segundo da criação, remetente incorreto, evento antigo, revogação e callback fora de ordem.
- Shadow sem acesso ao provedor, convite expirado, template não aprovado fora da sessão, limite diário de pedidos e lease perdido.
- Timeout ambíguo não causa repetição da discagem; permanece para reconciliação.
- Assinatura contra o exemplo oficial da Twilio, rejeição de callback adulterado, corpo REST de chamadas e consulta autenticada da data da permissão.
- Stream de uso único, encaminhamento de áudio inicial, confirmação de reprodução e interrupção. Marks devolvidas após `clear` não contam como áudio ouvido.
- Opt-out falado encerra a sessão e publica o evento comercial. Contexto persiste somente as duas transcrições válidas do cenário sem criar uma Run de texto.
- Reserva sem transcrição é rejeitada; reserva validada entra na fila de agendamento e só esse worker a executa. Confirmação textual aguarda a chamada encerrar.
- Formulário com receita zero publica intenção automática quando a opção está habilitada.

O smoke test dos seis workers (`six-workers-smoke-result.json`, evidência local não versionada) iniciou **sete processos**: API, contexto, execução, mídia, agendamento, follow-up e voz. A intenção de chamada permaneceu parada até iniciar voz, terminou como `simulated` e não foi duplicada. Todos encerraram normalmente via SIGTERM. Zero mensagens, zero ligações externas e zero chamadas OpenAI. O `.env` do usuário não foi carregado nesse teste.

A presença da chave OpenAI foi conferida sem exibir seu valor; não foi validada com uma requisição real. Credenciais Twilio, remetente habilitado, template, WSS público, modelos e qualidade conversacional real continuam pendentes de homologação. O serviço permanece com voz desativada por padrão. Consulte os limites operacionais em [voice.md](voice.md).

O JSON n8n permanece com 127 nós e SHA-256 `f3c99fd421681c0102f48f12aec4373a9ed10fa9729aee1ffbd92de3f0fa5f97`.
