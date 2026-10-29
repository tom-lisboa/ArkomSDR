# ArkomSDR

Serviço novo e independente para captar leads, conversar, organizar agendamentos e acompanhar comparecimento. Implementado do zero a partir da análise do fluxo exportado e dos problemas relatados em 27/10/2026.

**Estado atual:** núcleo executável, API, fila persistente, agenda própria e testes locais. O padrão é `shadow`: registra decisões e ações propostas, sem enviar WhatsApp, reservar em serviços externos ou alterar o n8n. As integrações antigas ficaram para depois, conforme a decisão desta conversa. Não é uma substituição de produção já homologada.

**Publicação de homologação:** API, seis workers, PostgreSQL 17 e Redis 7 publicados no servidor `24.199.100.186`, com HTTPS. O ambiente de teste recebe conversas iniciadas por diferentes números, com [integração direta Twilio](docs/whatsapp.md). Retomadas comerciais gerais estão desligadas; preparação e lembretes de reunião estão ativos na configuração conferida em 29/10/2026. O [webhook do formulário de teste](docs/test-form-webhook.md) cadastra os dados e pode iniciar o contato automaticamente, sem mensagem prévia do cliente; configure a URL e a chave no seu ambiente. Fora da janela de atendimento, o primeiro contato depende de template aprovado. Áudio e imagens, agrupamento de mensagens e digitação estão integrados; veja [mídia no WhatsApp](docs/whatsapp-media.md). Consulte [operação, verificações e pendências](docs/deployment.md).

**Mapa da jornada:** [fluxo completo em Mermaid para Obsidian](docs/fluxo-cliente-mermaid.md), com caminhos do cliente, follow-ups, exceções, workers e proposta de evolução dos agentes. Os diagramas individuais estão em [docs/mermaid](docs/mermaid).

**Especialistas na mesma conversa:** [Decisions, roteamento Go e memória comercial](docs/specialists.md). Cada turno escolhe entre persuasão, objeções, agenda, comparecimento e recuperação, com prompts próprios e uma única identidade da Bia. A ativação é explícita por `BIA_SPECIALISTS_ENABLED`.

**Case no início do contato:** o [vídeo Camozzi](docs/intro-video.md) é enviado pelo worker de mídia após a entrega da abertura, uma vez por contato. Fora da janela de atendimento, usa um template de vídeo aprovado próprio.

## Mudanças comerciais implementadas

- O formulário gera trabalho imediatamente; não depende de uma primeira mensagem do lead.
- Receita zero ou ausente não bloqueia o atendimento nem o acesso ao próximo passo de agendamento.
- Site da empresa e tamanho da equipe não fazem parte do formulário mínimo.
- O cadastro responde com uma ação de agendamento; a disponibilidade pode ser consultada por API.
- Agenda vazia (`empty`) é diferente de falha na consulta (`unavailable`). Nos dois casos há caminho para registrar preferência.
- Confirmações, lembretes e recuperação após falta usam o estado real da reserva.
- O no-show só é marcado por um evento de presença informado; silêncio ou ausência de clique não provam falta.

A API foi construída. O site mostrado nas imagens **não foi editado**. É necessário integrar seu formulário à API para mudar a experiência real.

## Rodar sem banco, sem chave e sem rede

```sh
go run ./cmd/replay -input examples/replay.jsonl
```

O replay usa eventos sintéticos, relógio explícito e decisões fornecidas no cenário. Mostra cadastro com receita zero, preferência de horário, oferta, escolha, reserva e opt-out. Não chama uma IA nem executa as ações apresentadas.

## Rodar a API local

Requisitos: Go 1.26+, PostgreSQL 17 e Redis 7 para o buffer rápido (há recuperação pelo Postgres). O SDK solicitado está fixado em `github.com/openai/openai-go/v3 v3.89.0`, que exige Go 1.26. O Go instalado pode baixar essa toolchain automaticamente.

```sh
cp .env.example .env
# Edite .env e preencha BIA_API_TOKEN com: openssl rand -hex 32
docker compose up -d postgres redis
set -a
source .env
set +a
go run ./cmd/bia -migrate
go run ./cmd/bia
```

O comando de migração só cria o schema `bia_go` no banco configurado. Não há migração automática no startup normal. A configuração de banco no Compose é exclusiva para desenvolvimento local.

```sh
curl http://127.0.0.1:8080/healthz
curl -X POST http://127.0.0.1:8080/v1/forms \
  -H "Authorization: Bearer $BIA_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: form-demo-001' \
  --data-binary @examples/form.json
```

O resultado traz `lead_id`, `event_id`, `contact_status` e `next_step`. Consulte o processamento em `GET /v1/jobs/{event_id}` e o contato em `GET /v1/leads/{lead_id}`, com a mesma autenticação. O status `queued_for_simulation` não significa mensagem enviada.

**A chave da API é de servidor.** O navegador deve passar pelo backend do site; não embuta esse token no JavaScript público. Os endpoints de eventos são contratos internos, não webhooks públicos de WhatsApp prontos para receber qualquer provedor.

## Seis workers

Contexto gerencia histórico, janela, Redis e `previous_response_id`; execução escolhe o agente; mídia prepara anexos; agendamento executa reservas, remarcações e cancelamentos; follow-up cuida de retomadas, lembretes e recuperação após falta; voz solicita autorização nativa no WhatsApp e conduz uma chamada de IA apenas para lembrar uma reunião existente, consultando o worker de agenda. Consulte o [contexto compartilhado e as regras atuais](docs/reminder-context.md). Os papéis são `-role context`, `-role execution`, `-role media`, `-role scheduling`, `-role followup` e `-role voice`, além da API com `-role api`. O padrão `-role all` executa tudo localmente. Consulte [contratos, limites e comandos](docs/workers.md) e [configuração de voz Twilio + OpenAI Realtime](docs/voice.md). Voz permanece desativada por padrão; no servidor de teste, a primeira chamada autorizada foi concluída e confirmada pelo usuário.

## OpenAI e mídia

Defina `OPENAI_API_KEY` e `OPENAI_MODEL` para ativar o planejador com Responses API e JSON Schema estrito. O modelo propõe uma decisão; o código valida e executa. Não existe execução de SQL ou shell gerado pelo modelo. Sem chave, o modo limitado reconhece pedidos simples de agenda e encaminha o restante para revisão.

Para áudio, configure também `OPENAI_AUDIO_MODEL` e `BIA_MEDIA_HOSTS`, separados por vírgulas. A transcrição usa o SDK Go. Downloads aceitam somente HTTPS, hosts permitidos, IPs públicos e arquivos de até 20 MiB; não seguem redirecionamentos. Falha de transcrição gera revisão, sem inventar o conteúdo do áudio.

Imagens e PDF usam `OPENAI_MEDIA_MODEL` no worker de mídia. Runs conversacionais usam `store: true` e `previous_response_id` por agente; a extração de mídia usa `store: false`. O Postgres conserva o histórico canônico e permite reconstruir uma janela limitada.

Referências utilizadas: [SDK Go oficial](https://developers.openai.com/api/reference/go) e [Structured Outputs](https://developers.openai.com/api/docs/guides/structured-outputs). A decisão do modelo é validada novamente no domínio; o formato estruturado não substitui as regras comerciais.

## Agenda própria

O [atendimento por WhatsApp](docs/whatsapp-scheduling.md) usa um prompt separado do roteiro de ligação e conduz o lead à oferta de dois horários reais. O worker de agenda pode manter disponibilidade diária em uma janela móvel, com duração, intervalos e fuso configuráveis.

`POST /v1/slots` publica uma vaga na agenda local. Informe `id`, `start`, `end`, `closer_id` e, quando houver, `meeting_url`; datas usam RFC3339 com fuso. O exemplo de produto adota 45 minutos, conforme as imagens. A duração efetiva é `end - start`, sem um segundo valor escondido no prompt.

`GET /v1/leads/{id}/availability` devolve as vagas. Para selecionar, envie um evento `message.received` com `selected_slot_id` em `/v1/events`. O servidor confere disponibilidade e identidade do closer. Reserva e liberação da vaga antiga acontecem na mesma transação no calendário local.

A agenda local não sincroniza Google Calendar, não gera links de Meet e não envia convites por e-mail. Na homologação atual, a reserva fica somente no PostgreSQL e a confirmação informa dia/hora, sem link de sala. Em `shadow`, a reserva também é apenas proposta: não consome a vaga. Os testes de integração exercitam a reserva real dentro do banco de testes isolado.

## Organização

| Caminho | Responsabilidade |
|---|---|
| `cmd/bia` | Serviço HTTP, workers, migração explícita e encerramento |
| `cmd/replay` | Avaliação determinística sem rede |
| `internal/domain` | Estado, regras, calendário de lembretes e autorização de ações |
| `internal/agent` | SDK OpenAI v3, prompt e schema da decisão |
| `internal/store` | Inbox/outbox, idempotência, leases e métricas no Postgres |
| `internal/worker` | Worker de execução, seleção de agente e ações comerciais |
| `internal/contextworker` | Worker de contexto, agrupamento no Redis e recuperação pelo Postgres |
| `internal/mediaworker` | Worker de mídia e publicação dos resultados |
| `internal/schedulingworker` | Reservas, remarcações, cancelamentos e eventos de agenda |
| `internal/followupworker` | Cadência, lembretes, recuperação e horários de contato |
| `internal/voiceworker` | Permissão nativa, chamadas, áudio Realtime e ferramentas de voz |
| `internal/conversation` | Contratos de contexto, execução e mídia |
| `internal/providers` | Agenda/CRM locais, áudio e adaptadores futuros |
| `internal/httpapi` | Entrada do formulário, eventos, vagas e inspeção |
| `internal/integration` | Testes com Postgres e HTTP locais |
| `docs` | Diagnóstico, contratos e limites da migração |

## Testes

```sh
go test -race ./...
go vet ./...
go build ./cmd/bia
```

Os testes de integração exigem explicitamente um banco local chamado `arkom_bia_test`. **Esse banco é descartável:** a suíte recria seu schema `bia_go`.

```sh
BIA_TEST_DATABASE_URL='postgres://usuario@127.0.0.1:5432/arkom_bia_test?sslmode=disable' \
  go test -race ./...
```

Sem essa variável, os testes de integração são sinalizados como ignorados, não como comprovados. Os testes não usam chaves reais nem enviam mensagens externas.

## Antes da operação real

O padrão local é simulação; o servidor de homologação usa Twilio em modo live. Antes de substituir a operação existente, ainda é necessário homologar a disponibilidade dos especialistas, integrar as origens dos eventos e avaliar conversas reais anonimizadas. `BIA_MODE=live` exige gateway e URL de agenda reais no modo legado; o adaptador Twilio usa envio direto e pode consultar a agenda local sem URL externa. Cada contato ainda precisa ter `owner=go`; o padrão é `shadow`. Isso impede assumir contatos do n8n por acidente.

Leia [contratos de integração](docs/integrations.md), [diagnóstico e critérios](docs/diagnosis.md) e [mapa de migração](docs/migration.md). Nenhum ganho de comparecimento foi medido em produção nesta etapa.

## Follow-up da reunião

Implementado em código, com ativação separada e desligada até instalar a gravação:

- Trinta minutos antes da reunião: enviar o áudio pronto fornecido pela equipe, usando o worker de mídia.
- Quinze minutos antes: enviar a mensagem de lembrete e solicitar autorização de chamada. A ligação só pode ocorrer após autorização nativa válida.
- O worker de follow-up coordena a sequência no PostgreSQL, confere a reserva atual e impede duplicidade após reinício, remarcação ou cancelamento.

A gravação `1-daqui-a-pouco-A.mp3` foi instalada como OGG/Opus no servidor em 28/10/2026; a cadência T−30/T−15 está ativada. Consulte [instalação, ativação e limites da janela do WhatsApp](docs/followups.md). Os comprovantes operacionais e as credenciais permanecem nos arquivos locais ignorados pelo Git.

Para apresentações, os [endpoints de demonstração](docs/demo-triggers.md) permitem
acionar o áudio e o fluxo de autorização/ligação por dois botões no formulário,
sem esperar os horários T−30/T−15 e sem mudar a reunião no banco.

O [lembrete SMS em T−5](docs/sms-reminders.md) usa o mesmo worker de follow-up,
com autorização própria no formulário e botão opcional `/reminders/sms` para demonstração.

Disponibilidade e reserva pelo formulário: [contrato dos endpoints](docs/form-calendar.md).

Abertura com confirmação da reserva, preparação após 15 minutos e correção dos erros do formulário: [contrato e diagnóstico](docs/test-form-troubleshooting.md).
