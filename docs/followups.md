# Lembretes da reunião

Implementação própria em Go, PostgreSQL e worker de mídia. A agenda e os demais
workers existentes continuam sendo a fonte dos dados; o n8n não participa.

Para apresentações, os [disparos manuais do formulário](demo-triggers.md) antecipam
uma etapa por clique usando os mesmos workers, mesmo com a cadência desligada.

## Cadência ampliada — 30/10/2026

As retomadas da abertura agora têm uma sequência própria, independente das antigas retomadas de oferta que continuam desligadas. O relógio começa na abertura realmente entregue, e não no envio do formulário.

| Sem resposta e sem reserva | Ação e resposta direta |
|---|---|
| 15 minutos | Retoma a abertura e oferece Ver horários, Tirar dúvida ou Agora não. |
| 2 horas | Explica a utilidade da conversa e facilita a escolha do próximo passo. |
| Dia seguinte | Confere se ainda faz sentido seguir com o diagnóstico. |
| 3 dias | Última tentativa da sequência; informa que não vai continuar insistindo. |

Uma resposta do cliente, reserva, atendimento humano, pausa, falta de consentimento ou saída invalida a sequência. `Agora não` pausa contatos e recebe uma confirmação curta. Um botão da campanha antiga não pode pausar uma reserva nova. Uma nova abertura tem identidade própria; jobs antigos não reaproveitam esse contexto.

As retomadas respeitam 08h–20h no fuso do contato. Etapas adiadas para a manhã seguinte ficam separadas por pelo menos 90 minutos. Jobs vencidos não viram uma rajada de compensação após reinício. `BIA_NO_REPLY_SINCE` define o corte explícito de ativação: cadastros antigos não são incluídos automaticamente no rollout.

Novos controles: `BIA_NO_REPLY_ENABLED`, `BIA_NO_REPLY_SINCE` e `BIA_MEETING_CHECKINS_ENABLED`. O processo continua sendo `followup`, com os mesmos leases, outbox e limites de envio. Os propósitos novos aparecem no histórico da API de gestão e nas métricas de follow-ups; o status privado `GET /v1/followups` informa ativação e horários das duas novas cadências.

Os oito modelos específicos estão em `deploy/cadence`: quatro retomadas comerciais submetidas como `MARKETING` e quatro avisos transacionais de reunião submetidos como `UTILITY`. Fora da sessão recente, cada envio confere o modelo exato e a aprovação atual. Com aprovação pendente, adia o mesmo job até o prazo original; não usa corpo livre nem o modelo de sessão para contornar a janela. A versão inicial foi rejeitada por começar com variável; os corpos v2 incluem uma abertura literal e são os usados nesta implementação. `deploy/provision-cadence.py` cria/submete os modelos sem enviar mensagens. `deploy/publish-proactive.py` exige as quatro retomadas aprovadas e aceita avisos de reunião aprovados ou em análise. Publica o binário testado e ativa as flags preservando o ambiente e os dados. Dentro da sessão, os botões de sessão já podem atender os novos avisos; fora dela, modelos de reunião pendentes permanecem bloqueados por envio até aprovação ou expiração. Não é necessário novo deploy quando a aprovação chega.

**Publicação em 30/10/2026:** release `r-22e51efb15a9`, migração 19. As duas flags novas foram ativadas; a retomada da abertura usa corte de ativação em 30/10/2026 às 13:53 de Cuiabá, preservando contatos anteriores contra inclusão retroativa. No preflight, as quatro retomadas estavam aprovadas e os quatro avisos de reunião estavam em análise. Esse status externo pode mudar depois; o transporte consulta a aprovação no momento de cada envio. A suíte completa com PostgreSQL e `go test -race ./...`, `go vet` e o build Linux passaram. A verificação não disparou mensagens de demonstração para clientes.

| Momento | Responsável | Ação |
|---|---|---|
| 15 minutos após a abertura | Follow-up | Pede um exemplo prático para preparar a reunião, sem depoimento. |
| T−24 horas | Follow-up → WhatsApp | Lembra data/duração e oferece confirmação, remarcação ou ajuda. |
| T−2 horas | Follow-up → WhatsApp | Reforça o horário e pede uma resposta por botão. |
| T−30 minutos | Follow-up → mídia | Envia a gravação da equipe como áudio OGG/Opus. |
| T−15 minutos | Follow-up | Envia texto com horário local; inclui link apenas se existir na reserva. |
| Após confirmar o envio do texto | Follow-up → voz | Solicita permissão pelo botão nativo do WhatsApp. |
| Após o cliente permitir | Voz → agenda | Confere a reserva e inicia uma única ligação de lembrete. |
| T−5 minutos | Follow-up → SMS | Envia lembrete por SMS aos contatos que autorizaram esse canal. |
| T−5 minutos | Follow-up → WhatsApp | Acrescenta o aviso curto com botões, independente do consentimento de SMS. |
| T: início da reunião | Follow-up → WhatsApp | Informa o horário real e pede resposta CONFIRMO ou REMARCAR. |
| T+7 minutos | Follow-up → WhatsApp | Oferece ajuda se não houve resposta ou confirmação desde T−15; não declara falta. |

Os quatro novos avisos trazem Confirmar presença, Remarcar e Preciso de ajuda. T−15 também usa esses botões antes da solicitação nativa de autorização da chamada. Nunca existem duas ligações por acrescentar avisos de texto. Não há promessa de sala aberta ou consultor esperando; um link só pode ser oferecido quando existir na reserva.

Os avisos novos de 24h e 2h respeitam os horários de contato e expiram respectivamente em 30 e 15 minutos. Não são gerados se coincidirem com os 15 minutos seguintes a uma abertura/reserva recente. T−5 vale até T−1 e T+7 até T+12, sempre antes do fim previsto. O aviso final não altera comparecimento: esse resultado continua dependendo de registro explícito da equipe.

O [SMS](sms-reminders.md) tem ativação independente e não depende do áudio instalado
nem da janela de atendimento do WhatsApp.

O WhatsApp no início tem ativação independente por `BIA_MEETING_START_ENABLED` e usa `TWILIO_MEETING_START_CONTENT_SID` aprovado quando não há sessão recente. O job fica elegível no início real da reserva e expira cinco minutos depois, limitado também ao fim da reunião. A reconciliação recupera reinícios nesse intervalo sem repetir uma etapa já concluída. O aviso continua sujeito a processamento da fila e entrega do provedor.

Exemplo de texto, com valores gerados a partir do cadastro e da reserva:

> Oi, Ana! Sua reunião com a Arkom é agora: 30/10 às 14:00. Separe 45 minutos para avaliar a IA no seu negócio.
>
> Você participa agora? Responda CONFIRMO ou REMARCAR.

Não há anúncio de sala aberta nem de consultor esperando. Este aviso não inclui link de reunião. `CONFIRMO` registra intenção de comparecer, inclusive no intervalo da reunião, sem marcar presença real. `REMARCAR` consulta novas vagas e preserva a reserva atual até a troca concluir. Durante esse intervalo o contexto usa `meeting_status=in_progress`, evitando uma nova oferta automática por simples confirmação ou atraso.

O texto de T−15 passou a trazer horário, duração e benefício da conversa antes do pedido de autorização da ligação, sem reapresentar a Bia. Os prompts de texto `bia-specialists-v2` orientam uma chamada específica para ação quando cabe avançar; agradecimento, pausa, recusa e saída não geram outro convite. Condições e resultados comerciais não são inventados para obter resposta.

O botão não autoriza a ligação sozinho: a resposta nativa precisa ser validada.
Sem aceite, não há chamada. A ligação pode começar após T−15, quando o cliente
aceitar, mas nunca depois do início da reunião. A IA de voz continua limitada a
lembrar a reunião e registrar dúvidas; não remarca nem cancela por conta própria.

A preparação tem ativação independente por `BIA_MEETING_PREPARATION_ENABLED` e usa `TWILIO_PREPARATION_CONTENT_SID` aprovado fora da janela de atendimento. Só ocorre com reserva futura, respeita horário de contato e termina antes dos 30 minutos finais da reunião. [Detalhes e recuperação após reinício](test-form-troubleshooting.md#preparação-15-minutos-após-a-abertura).

## Estado de ativação

Em 29/10/2026, o aviso de início foi publicado e habilitado na homologação, release `r-36043e9ddb53`, migração 16. A verificação após publicação confirmou os sete serviços ativos, `/readyz` disponível e três jobs futuros na fila `followup`. A Twilio confirmou o template `arkom_reuniao_agora_v1` como `approved`, categoria `UTILITY`, permitindo também o caminho fora da sessão. Esse é um retrato da publicação, não um status atualizado automaticamente; a aprovação é reconsultada antes de cada envio por template.

`BIA_MEETING_REMINDERS_ENABLED=false` continua sendo o padrão de instalação.
No servidor de teste, a cadência foi ativada em 28/10/2026 após instalar a gravação
`1-daqui-a-pouco-A.mp3` fornecida pelo usuário: 19,28 segundos, convertida para
OGG/Opus mono 48 kHz. O arquivo permanece fora do Git e foi validado por checksum,
decodificação completa e acesso HTTPS público.
O código usa uma margem conservadora de **23 horas desde a última mensagem recebida**.
T−30 e T−15 continuam dependendo dessa sessão: fora dela, registram
`approved_template_required_outside_session` e não enviam áudio, texto ou botão.
O aviso no início tem template próprio `arkom_reuniao_agora_v1`; fora da sessão,
o adaptador verifica a aprovação e adia o mesmo job enquanto estiver pendente,
sem ultrapassar sua validade. A preparação após a abertura também tem template próprio.

A cadência é independente de `BIA_DISABLE_FOLLOWUPS=true`, que continua bloqueando
as retomadas antigas. Quando a cadência nova está ligada, os antigos lembretes de
24h/2h/15min são substituídos, evitando mensagens sobrepostas. Uma restrição
`BIA_TEST_PHONE` impede sua ativação geral.

## Colocar a gravação

1. No computador de desenvolvimento, converter o arquivo fornecido:

   ```sh
   python3 scripts/prepare-reminder-audio.py "/caminho/para/audio-original.mp3"
   ```

   Requer `ffmpeg`. Aceita também WAV/M4A/OGG e gera OGG/Opus mono, até 16 MiB,
   em `.local/media/reminders/<sha256>.ogg`, fora do Git. Não sintetiza outra voz.

2. Enviar o arquivo resultante ao servidor e instalar:

   ```sh
   scp -i ~/.ssh/arkom_dev -o IdentitiesOnly=yes .local/media/reminders/<sha256>.ogg dev@24.199.100.186:/tmp/reminder.ogg
   ssh -i ~/.ssh/arkom_dev -o IdentitiesOnly=yes dev@24.199.100.186
   sudo python3 /opt/arkom-bia/current/install-reminder-audio.py /tmp/reminder.ogg
   ```

   O instalador preserva as outras configurações e credenciais. Define hash e
   diretório, mantendo a cadência desligada. Arquivo permanente:
   `/var/lib/arkom-bia/media/reminders/<sha256>.ogg`, montado somente para leitura
   nos containers como `/media/reminders`. Copie esse diretório junto aos backups
   caso migre o servidor; o backup de PostgreSQL não inclui arquivos de áudio.

3. Para aplicar o arquivo, ainda sem iniciar a cadência:

   ```sh
   cd /opt/arkom-bia/current
   sudo docker compose up -d api context execution media scheduling followup voice
   ```

   Conferir GET/HEAD público em `https://24.199.100.186/media/reminders/<sha256>.ogg`
   e GET privado `/v1/followups` com Bearer administrativo. O status precisa mostrar
   `audio_ready=true`. Não coloque tokens administrativos em páginas públicas.

4. Após ouvir e validar o arquivo, alterar **somente**
   `BIA_MEETING_REMINDERS_ENABLED=true` em `/etc/arkom-bia/runtime.env` e recriar
   os sete serviços pelo comando acima. Manter `BIA_DISABLE_FOLLOWUPS=true` se as
   campanhas antigas ainda não forem desejadas. A ativação exige voz Twilio
   configurada e o hash correto de um arquivo OGG/Opus presente no servidor.

## Persistência e controles

- Horários persistidos em UTC; quiet hours 08h–20h no fuso do cliente (ou padrão).
- Tolerância de 5 minutos para cada disparo: áudio até T−25, texto/botão até T−10.
  Atrasos maiores são descartados, sem tentar compensar depois da reunião.
- O aviso de início tem intervalo próprio T até T+5 minutos, sempre antes do fim da reserva. Atrasos maiores não são recuperados.
- No início, o envio solicitado para o horário reservado não é deslocado pelas quiet hours; consentimento, pausa, humano e validade continuam sendo verificados.
- Reconciliação a cada 30 segundos e na partida inclui reservas anteriores à
  ativação. IDs derivados de cliente, reserva, horário e etapa impedem duplicidade.
- Opt-out, pausa, atendimento humano, troca de reserva e cancelamento são
  rechecados antes de cada etapa. Convites pendentes da reserva antiga são invalidados.
- Enquanto houver mensagens/áudios do cliente sendo agrupados, o disparo espera,
  preservando o prazo original. Uma conversa de voz ativa também adia os envios.
- Os jobs preparados em shadow nunca viram envios reais. Uma etapa já encerrada
  como skipped/simulated não é ressuscitada pela reconciliação.
- Áudio e texto compartilham limites diários/globais e um registro anterior ao POST
  da Twilio. Resultado incerto exige revisão; não existe reenvio automático cego.
- Falha no aviso opcional de início é registrada sem colocar o WhatsApp inteiro em atendimento humano.
- O hash identifica uma gravação imutável. A API verifica tamanho, cabeçalho
  OGG/Opus e SHA-256, suporta GET/HEAD/Range e não lista arquivos do servidor.
- Permissões continuam limitadas pelo worker de voz (uma solicitação por 24h,
  duas por semana). O agendador não reaproveita autorização para ligar repetidamente.

## Verificação

Esta atualização passou pela suíte completa com `go test -race ./... -count=1`, incluindo integração com PostgreSQL isolado, e por `go vet ./...`. Os testes do aviso verificam janela/template, aprovação pendente, execução única após reinício, fila correta, prazo, cancelamento, remarcação e bloqueios do contato. Quatro turnos sintéticos com API real validaram consulta do horário, atraso e escolha de uma nova vaga, sem enviar WhatsApp nem alterar a agenda de clientes. A experiência no aparelho com a nova etapa ainda precisa ser homologada.

`GET /v1/followups` (privado) informa ativação, áudio pronto e contagens por fila e
status. `GET /v1/jobs/{id}` informa resultado/motivo de descarte; IDs contendo `/`
devem ser codificados no segmento da URL. `GET /v1/voice/{id}` informa o convite e a
ligação. Os testes isolados cobrem sequência entre workers, remarcação, cancelamento,
permissão tardia, falha no texto, ambiguidade no envio do áudio, reinício e migração.
O arquivo está instalado e disponível para o botão manual de 30 minutos. A reprodução
no aparelho ainda precisa ser confirmada em um teste; a validação de publicação
não enviou mensagem de áudio por conta própria.

Referências: [mídia no WhatsApp pela Twilio](https://www.twilio.com/docs/whatsapp/tutorial/send-and-receive-media-messages-whatsapp-nodejs)
e [WhatsApp Business Calling](https://www.twilio.com/docs/voice/whatsapp-business-calling).
