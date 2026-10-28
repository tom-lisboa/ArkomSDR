# WhatsApp: áudio, imagem e mensagens em partes

O webhook assinado `/voice/messages` confere a mensagem na API da Twilio e lê os anexos no próprio webhook autenticado, cujo inventário pode estar à frente da API. Valida conta, mensagem pai, SID e URL de cada anexo. Salva apenas os identificadores e as URLs canônicas na fila durável. Downloads, transcrição e visão ficam no worker de mídia. O worker de contexto consolida o resultado e libera uma execução com todo o lote, preservando o histórico e o `previous_response_id` do agente.

## Comportamento

- Textos: aguarda **5 segundos** após a última mensagem recebida pelo contexto.
- Lote com áudio/imagem: aguarda **12 segundos** após a última mensagem, inclusive se a última for texto.
- Novas partes estendem a pausa até o limite de **30 segundos** desde o início do lote. Mídias pendentes continuam bloqueando a execução, mesmo depois desse limite.
- Após finalizar a leitura de uma mídia, reserva ao menos **3 segundos** para outra parte em trânsito. Espera todos os anexos do lote; o processamento pode terminar fora de ordem.
- Uma mídia sem resultado após **120 segundos** vira falha explícita. O SDR não inventa transcrição. O fluxo existente encaminha a situação para atendimento humano.
- Uma mensagem nova que chegue durante a geração invalida o envio da resposta antiga. O conteúdo permanece no histórico para a execução seguinte.
- Até 32 mensagens e 16.000 bytes de conteúdo por execução; limites maiores são divididos em turnos. Até cinco anexos por mensagem e 20 MiB por download.
- O WhatsApp só entrega o áudio ao backend depois de enviado. Não há detecção de uma gravação ainda em andamento; uma pausa maior que a janela pode abrir outro turno.

As durações são configuráveis em `BIA_CONTEXT_QUIET`, `BIA_CONTEXT_MEDIA_QUIET`, `BIA_CONTEXT_MEDIA_SETTLE`, `BIA_CONTEXT_MAX_WAIT` e `BIA_CONTEXT_MEDIA_TIMEOUT`. PostgreSQL é a fonte durável; Redis acelera a seleção das janelas. Redis indisponível não perde mensagens.

## Modelos e arquivos

`OPENAI_AUDIO_MODEL=gpt-4o-transcribe`: transcrição em português de notas de voz; OGG/Opus foi validado com uma amostra sintética real na OpenAI.

`OPENAI_MEDIA_MODEL=gpt-5.4-mini`: descreve imagens e extrai texto, datas e dúvidas. A extração é uma chamada separada, sem armazenamento de response na OpenAI (`store=false`) e sem alterar a cadeia de conversa. O texto extraído entra no contexto como conteúdo do usuário, nunca como instrução de sistema. Também existe suporte a PDF e texto simples; vídeo não é interpretado.

O download Twilio usa autenticação apenas na URL canônica da nossa conta. Redirecionamentos para CDN são acessados sem credenciais, somente por HTTPS e IP público validado no momento da conexão. Arquivos são limitados em tamanho, ficam em memória e não são gravados no disco. O histórico persistido contém texto extraído. A Twilio mantém sua própria cópia conforme as regras da conta.

`BIA_MEDIA_HOSTS` continua sendo a lista de hosts para fontes externas; não precisa incluir a Twilio. Credenciais e URLs temporárias de CDN não entram no histórico nem nos logs.

## Digitando

Um loop do worker de contexto aciona o [Typing Indicators da Twilio](https://www.twilio.com/docs/whatsapp/api/typing-indicators-resource) para conversas live sob controle do Go, com consentimento e sem opt-out, pausa ou atendimento humano. Respeita `BIA_TEST_PHONE` e não aciona enquanto houver uma ligação ativa.

O indicador também marca a mensagem como lida. É renovado no máximo uma vez a cada 15 segundos por conversa, com controle no PostgreSQL compartilhado entre processos. Para ao terminar a preparação; a Twilio remove o indicador ao entregar a resposta ou após 25 segundos. Um trabalho travado não o mantém indefinidamente: só mensagens de até três minutos são elegíveis. Falhar esse recurso não bloqueia a resposta.

## Validação

`go test -race ./...`, com PostgreSQL de teste isolado, verifica: webhook assinado com áudio e replay, inventário canônico, dois áudios + imagem + texto em um lote, espera por todos os resultados, fragmento ainda na fila, expiração, idempotência e throttle de digitação. Testes HTTP verificam isolamento de credenciais e bloqueio de URLs privadas. `go vet ./...` e compilação Linux também passam.

Amostras sintéticas reais de OGG/Opus e imagem: resultado (`media-smoke-result.json`, evidência local não versionada). O teste real recuperou dois áudios e uma foto enviados pelo usuário: os três ficaram prontos no worker de mídia e as respostas corrigidas foram enviadas. Como as entradas iniciais expuseram defeitos, houve recuperação técnica; a entrada de uma nova mídia após a última correção e a confirmação visual do indicador ainda precisam ser verificadas no aparelho.

Referências: [mídia da Twilio](https://www.twilio.com/docs/messaging/api/media-resource), [transcrição OpenAI](https://developers.openai.com/api/docs/guides/speech-to-text).

## Correções encontradas na homologação

Dois áudios reais chegaram à Twilio em 28/10/2026, às 10:42 de Cuiabá, mas o webhook inicial devolveu `message mismatch`. A conferência exigia o estado final `received`; passou a aceitar também `receiving`, o estado de entrada ainda em processamento documentado pela Twilio, preservando assinatura, destinatários, SID e corpo. Os logs de divergência agora mostram somente comparações booleanas e estado do provedor, sem conteúdo pessoal ou credenciais.

Os dois anexos foram recuperados pelos próprios IDs e transcritos com sucesso. A primeira execução revelou uma instrução antiga que induziu handoff mesmo com transcrição válida. A orientação do SDR agora define `[audio]` como fala já transcrita, `[image]`/`[document]` como leitura pronta e exige considerar todas as partes. O teste real do agente com as duas falas produziu `reply`: resultado (`media-agent-smoke-result.json`, evidência local não versionada).

No teste da imagem, a Twilio já enviava `NumMedia=1` e `MediaUrl0` no webhook enquanto o GET imediato ainda devolvia zero anexos. Agora a entrada usa o inventário assinado e validado do webhook, sem depender dessa atualização eventual. O teste de regressão reproduz explicitamente `receiving` + zero mídias REST + um anexo assinado.

O endpoint de digitação exige `channel=WHATSAPP` na execução observada (o texto da tabela da documentação sugere minúsculas, mas o exemplo usa maiúsculas). Minúsculas retornaram 400; maiúsculas retornaram HTTP 200 e `success=true`. O adaptador e os testes foram corrigidos para o valor aceito.

A recuperação dos dois áudios só reabriu mensagens sem tentativa de resposta enviada, com guarda para opt-out/pausa, trabalhos ativos e operação repetida. A nota `repair/media-batch-20261028` registra o ajuste no CRM. A foto, que já havia gerado uma resposta sem leitura, recebeu o evento corretivo idempotente `media-recovery-MMfc5f58d07454a89e420524830f9a47d1-v1`, baseado na mídia verificada e na última mensagem real, preservando o horário original de atendimento. Nenhuma dessas recuperações fabricou uma mensagem recebida da Twilio nem alterou o n8n.

Resultado final da recuperação: dois áudios e uma imagem com status `ready`, sem erro de mídia; resposta ao lote de áudios enviada e resposta corretiva à foto enviada. O payload completo de um webhook real de áudio foi reapresentado à versão final: HTTP 200, sem duplicar entrada ou mídia. Evidência resumida: homologação (`media-live-test-result.json`, evidência local não versionada).

## Respostas em partes

As respostas conversacionais (`purpose=reply`) podem sair em até três mensagens. A orientação da Bia pede blocos com uma ideia completa. O código preserva respostas curtas (até 180 caracteres), palavras, links e listas estruturadas; não transforma cada frase curta em uma notificação.

O worker de execução prepara um plano de envio persistente. Cada bloco tem ID próprio (`:part:1`, `:part:2`, `:part:3`), reutiliza o registro de envio da Twilio e só publica o próximo bloco após confirmar o envio atual. A pausa de 2 a 4 segundos fica no `due_at` do próximo trabalho, sem bloquear um worker em espera. O indicador de digitação é solicitado entre blocos; uma falha desse indicador não bloqueia o texto.

Cada bloco passa novamente por opt-out, controle humano, propriedade do contato, validade, sessão de voz, limites de envio e versão da conversa. Uma mensagem nova interrompe os blocos restantes; as partes já enviadas continuam no histórico. Falha ou envio incerto interrompe a sequência e segue para revisão, sem reenviar automaticamente uma parte de resultado desconhecido.

Somente os blocos efetivamente enviados entram no histórico. O trabalho que organiza a resposta não conta como uma mensagem enviada; cada bloco real conta nos limites existentes. Confirmações de agendamento, lembretes e outros avisos operacionais mantêm seus formatos.

Testes com PostgreSQL e detector de concorrência cobrem envio ordenado, pausa, retomada em outra instância de worker, interrupção por nova entrada, falha incerta no segundo bloco e histórico contendo apenas o primeiro bloco enviado. Testes de conteúdo verificam preservação de links, Unicode, listas e limite de três blocos.
