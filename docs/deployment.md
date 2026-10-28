# Publicação no servidor Arkom

Publicado em 28/10/2026 em `dev@24.199.100.186`, Ubuntu 24.04. Release inicial `r-e26c3d76ca51`; atual `r-c912a41f4a60`, com integração direta Twilio. O binário foi compilado para Linux amd64 a partir do código local. O n8n não foi alterado.

## Estado da infraestrutura

- API e seis processos independentes: contexto, execução, mídia, agendamento, follow-up e voz.
- PostgreSQL 17 com banco `bia`, schema `bia_go`, migrações 1–6; Redis 7 com AOF.
- Containers com reinício automático, limites de memória e logs com rotação. Aplicação sem root e com filesystem somente leitura.
- PostgreSQL/Redis não publicam portas no host. API e voz escutam somente em `127.0.0.1:8080/8081` no host.
- Caddy publica somente `/healthz` e `/voice/*` por HTTPS. `/v1/*` permanece privado e exige token também no acesso local.
- HTTPS válido para o IP, certificado Let's Encrypt `shortlived` com renovação pelo Caddy. Foi validado externamente sem desabilitar a verificação TLS.
- Atualização: modo `live` restrito pelo `BIA_TEST_PHONE` ao número de teste autorizado. Voz automática de formulário e follow-ups programados desativados. Isso é um ambiente de homologação, não uma migração concluída do atendimento. Veja [WhatsApp](whatsapp.md).

Status público: <https://24.199.100.186/healthz>.

A chave OpenAI corrigida autenticou com HTTP 200 localmente e a partir do servidor. O teste real do agente Go com `gpt-5.4-mini` recebeu resposta válida no schema do SDR, com dados sintéticos. Ver resultado (`openai-smoke-result.json`, evidência local não versionada). A chave e o modelo foram configurados no ambiente remoto e os sete processos foram recriados; readiness voltou a responder 200. Modelo escolhido para a homologação inicial; não houve comparação de qualidade entre modelos. Após validação, o teste de WhatsApp foi ativado em modo live restrito ao número autorizado, sem follow-ups programados.

## Configuração Twilio

O aplicativo `AP0803d2a6885b47f8490368886475304f`, associado ao remetente `+5511936246321`, passou a usar **POST `https://24.199.100.186/voice/incoming`**. Alteração feita via API e confirmada por GET.

Essa rota valida a assinatura Twilio, a conta e o destinatário. Retorna `<Response><Hangup/></Response>`: atendimento de ligações recebidas por IA ainda não está implementado. A mudança substitui o Echo Twimlet anterior, preservando o comportamento de encerrar chamadas recebidas.

`/voice/permission` é o callback dedicado de autorização nativa, **não** a Voice Request URL do aplicativo. A integração atual usa `/voice/messages`, que roteia essa autorização e mensagens comuns sem invalidar a assinatura. `BIA_VOICE_ENABLED=true` habilita a chamada de teste, condicionada ao botão nativo e limitada ao destinatário autorizado. O webhook de mensagens anteriormente vazio foi configurado via API.

Recebimento/envio de texto Twilio foi integrado e os modelos Realtime/transcrição foram configurados. Uma ligação autorizada foi concluída, com áudio bidirecional confirmado pelo usuário. Ainda faltam publicar horários reais e homologar uma reserva completa. Veja o resultado do teste (`voice-live-test-result.json`, evidência local não versionada).

Áudio, imagem, agrupamento de mensagens e indicador de digitação também estão publicados. Modelos de mídia passaram por teste real com arquivos sintéticos; dois áudios e uma foto reais foram processados após recuperação dos defeitos encontrados. A entrada automática de nova mídia na versão final e a exibição visual da digitação aguardam confirmação no aparelho. Veja [mídia e contexto](whatsapp-media.md).

## Arquivos no servidor

- `/opt/arkom-bia/current`: link para o release ativo, com Compose e binário.
- `/etc/arkom-bia/runtime.env`: configuração do serviço, token da API e credenciais; acesso restrito a root.
- `/etc/arkom-bia/postgres.env`: credenciais do banco, restritas a root.
- `/etc/caddy/Caddyfile`: proxy e TLS. Configuração anterior em `/etc/caddy/Caddyfile.before-arkom`.
- `/var/backups/arkom-bia`: dumps locais e relatório do teste de publicação.

As credenciais ficam fora das imagens e dos releases.

## Operação

```sh
ssh -i ~/.ssh/arkom_dev -o IdentitiesOnly=yes dev@24.199.100.186
cd /opt/arkom-bia/current
sudo docker compose ps
sudo docker compose logs --tail 50 execution voice
curl --fail http://127.0.0.1:8080/readyz
```

Para acessar a API privada pelo computador local:

```sh
ssh -i ~/.ssh/arkom_dev -o IdentitiesOnly=yes -N -L 18080:127.0.0.1:8080 dev@24.199.100.186
```

Use `http://127.0.0.1:18080` e o token do servidor somente no backend/terminal. Não coloque o token no navegador ou no site público.

Uma alteração em `runtime.env` exige recriar os containers, não apenas `restart`:

```sh
cd /opt/arkom-bia/current
sudo docker compose up -d --force-recreate api context execution media scheduling followup voice
```

O diretório `deploy/` no projeto contém os arquivos da publicação. `activate.sh` deve ser executado como root no novo diretório `/opt/arkom-bia/releases/r-*`, contendo binário Linux executável, `.env` com `BIA_RELEASE=r-*` e os arquivos de `deploy/`. Ele valida o Compose, constrói a imagem, tira backup se já houver release ativo, aplica migrações, sobe serviços, verifica readiness e atualiza `current`.

## Backup e verificação

Backup diário às 06:00 UTC (02:00 em Cuiabá), com até cinco minutos de dispersão, retenção local de aproximadamente sete dias. Comando manual: `sudo /opt/arkom-bia/current/backup.sh`. Timer: `arkom-bia-backup.timer`.

O dump foi restaurado em um banco temporário separado: quatro versões de schema e um lead sintético recuperados. O banco temporário foi removido depois da validação. O backup fica no mesmo servidor; ainda não existe cópia externa nem recuperação para um instante específico.

Teste inicial em shadow no servidor: cadastro com receita zero, processamento da fila, intenção de voz simulada, idempotência, agenda vazia e opt-out do contato sintético. Zero mensagens enviadas e zero chamadas reais. Ver resultado (`deployment-smoke-result.json`, evidência local não versionada).

Teste HTTP externo inicial, antes da ativação de voz: health 200, chamada recebida sem assinatura 403, assinatura válida 200 com Hangup, API administrativa pública 404, autorização/stream desativados 503. O teste de stream verifica bloqueio e roteamento, não uma sessão de áudio.

Testes locais: `go test -race ./...`, `go vet ./...` e compilação Linux passaram. Na integração WhatsApp, a suíte foi executada com `BIA_TEST_DATABASE_URL` e PostgreSQL 17 isolado, incluindo os testes de banco e fila.

Referências para TLS por IP: [Let's Encrypt](https://letsencrypt.org/2026/01/15/6day-and-ip-general-availability) e [configuração ACME do Caddy](https://caddyserver.com/docs/caddyfile/directives/tls).

Respostas conversacionais em até três blocos publicadas no release `r-c912a41f4a60`: sequência persistente, pausas de 2–4 segundos, digitação entre blocos e cancelamento de partes restantes quando chega nova entrada. Validação automatizada com PostgreSQL passou; a experiência visual da sequência ainda não foi confirmada no aparelho.

## Contexto compartilhado e lembretes — 28/10/2026

Release `r-c3c615bbfdb1`, schema 7, publicado e verificado. Os sete processos estão ativos. Texto e voz recebem empresa, pessoa, faturamento e data da reunião. O worker de voz consulta o de agenda por uma fila PostgreSQL e fica limitado a lembretes de reservas existentes. Testes com PostgreSQL e detector de corrida, vet e build Linux passaram. A API privada retornou os campos esperados; a fila de agenda recusou corretamente uma consulta de ligação antiga sem vínculo com reserva. Nenhuma chamada real foi disparada nessa validação. O cadastro de teste ainda não tem empresa, faturamento ou reserva preenchidos. Ver [integração](reminder-context.md) e evidências (`reminder-context-test-result.json`, evidência local não versionada).

## Webhook de formulário de teste — 28/10/2026

Release `r-3ed6c616d356`, schema 8. `POST /webhooks/test-form/{key}` publicado via Caddy com CORS e chave limitada ao formulário. Cadastros aguardam vinculação por mensagem WhatsApp assinada com código de uso único. Removida restrição `BIA_TEST_PHONE`; `BIA_DISABLE_FOLLOWUPS=true` mantém cadências desligadas. Chave OpenAI validada e sincronizada ao runtime. Limites de teste 100 mensagens/contato/dia e 300/hora global. As sete funções estão em execução. Detalhes em [webhook](test-form-webhook.md) e validação (`test-form-webhook-result.json`, evidência local não versionada).
