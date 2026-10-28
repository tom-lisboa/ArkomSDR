# Lembretes da reunião

Implementação própria em Go, PostgreSQL e worker de mídia. A agenda e os demais
workers existentes continuam sendo a fonte dos dados; o n8n não participa.

Para apresentações, os [disparos manuais do formulário](demo-triggers.md) antecipam
uma etapa por clique usando os mesmos workers, mesmo com a cadência desligada.

| Momento | Responsável | Ação |
|---|---|---|
| 15 minutos após a abertura | Follow-up | Pede um exemplo prático para preparar a reunião, sem depoimento. |
| T−30 minutos | Follow-up → mídia | Envia a gravação da equipe como áudio OGG/Opus. |
| T−15 minutos | Follow-up | Envia texto com horário e fuso; inclui link apenas se existir na reserva. |
| Após confirmar o envio do texto | Follow-up → voz | Solicita permissão pelo botão nativo do WhatsApp. |
| Após o cliente permitir | Voz → agenda | Confere a reserva e inicia uma única ligação de lembrete. |
| T−5 minutos | Follow-up → SMS | Envia lembrete por SMS aos contatos que autorizaram esse canal. |

O [SMS](sms-reminders.md) tem ativação independente e não depende do áudio instalado
nem da janela de atendimento do WhatsApp.

O botão não autoriza a ligação sozinho: a resposta nativa precisa ser validada.
Sem aceite, não há chamada. A ligação pode começar após T−15, quando o cliente
aceitar, mas nunca depois do início da reunião. A IA de voz continua limitada a
lembrar a reunião e registrar dúvidas; não remarca nem cancela por conta própria.

A preparação tem ativação independente por `BIA_MEETING_PREPARATION_ENABLED` e usa `TWILIO_PREPARATION_CONTENT_SID` aprovado fora da janela de atendimento. Só ocorre com reserva futura, respeita horário de contato e termina antes dos 30 minutos finais da reunião. [Detalhes e recuperação após reinício](test-form-troubleshooting.md#preparação-15-minutos-após-a-abertura).

## Estado de ativação

`BIA_MEETING_REMINDERS_ENABLED=false` continua sendo o padrão de instalação.
No servidor de teste, a cadência foi ativada em 28/10/2026 após instalar a gravação
`1-daqui-a-pouco-A.mp3` fornecida pelo usuário: 19,28 segundos, convertida para
OGG/Opus mono 48 kHz. O arquivo permanece fora do Git e foi validado por checksum,
decodificação completa e acesso HTTPS público.
Os templates aprovados para mensagens fora da janela de atendimento ainda não
estão integrados a esta cadência. O código usa uma margem conservadora de **23
horas desde a última mensagem recebida**. Com a janela fechada, registra
`approved_template_required_outside_session` e não envia texto, áudio ou botão.
Logo, o envio a todos os clientes em T−30/T−15 depende também dessa janela ou de
uma futura integração com templates aprovados; a cadência não contorna a regra.

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
- O hash identifica uma gravação imutável. A API verifica tamanho, cabeçalho
  OGG/Opus e SHA-256, suporta GET/HEAD/Range e não lista arquivos do servidor.
- Permissões continuam limitadas pelo worker de voz (uma solicitação por 24h,
  duas por semana). O agendador não reaproveita autorização para ligar repetidamente.

## Verificação

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
