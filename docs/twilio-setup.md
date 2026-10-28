# Configuração Twilio realizada via API

Data: 28/10/2026. Operações autorizadas nesta conversa para concluir a configuração sem o Console.

- Remetente: `+5511936246321`.
- Sender SID: `XEec1ee6cbe245f971d3824a3d18d4cdf3`.
- Estado confirmado por GET após a atualização: `ONLINE`.
- TwiML Application: `Arkom Bia Voice`, SID `AP0803d2a6885b47f8490368886475304f`.
- Método: `POST`.
- Voice Request URL atual: `https://24.199.100.186/voice/incoming`.
- Template: `arkom_permissao_ligacao`, idioma `pt_BR`, Content SID `HXc6c8ed88eda05371d5dc69d65b5fe68a`.
- Ação do template verificada por GET: `VOICE_CALL_REQUEST`.

A URL temporária do Echo Twimlet foi substituída após a publicação do servidor. O endpoint Go valida assinatura, conta e destinatário e retorna `<Response><Hangup/></Response>`. Chamadas recebidas continuam sendo encerradas, sem atendimento por IA. O aplicativo foi atualizado via API e conferido por GET. Consulte [a publicação](deployment.md).

A associação de voz foi aceita com HTTP 202 e estado `ONLINE:UPDATING`; uma consulta posterior confirmou `ONLINE` com o Application SID salvo. O objeto de configuração do webhook de mensagens foi comparado antes/depois e permaneceu igual. Nenhum remetente foi excluído, nenhuma mensagem ou ligação foi enviada e o n8n não foi alterado.

Credenciais e número foram configurados no `.env`, com acesso restrito ao proprietário. O Auth Token não é reproduzido neste documento. A configuração local permanece com `BIA_VOICE_ENABLED=false`, `BIA_VOICE_AUTO_REQUEST=false` e `BIA_VOICE_APPROVED_OUTSIDE_SESSION=false`.

## Pendências para uma chamada real

1. Publicação HTTPS concluída; `BIA_VOICE_PUBLIC_URL=https://24.199.100.186`. O stream WSS ainda precisa de homologação com voz ativada.
2. Configurar o recebimento de autorização nativa preservando o fluxo de mensagens existente e a validação de assinatura Twilio.
3. Configurar os modelos Realtime/transcrição e completar os requisitos de modo live do serviço.
4. Homologar com um contato de teste e autorização explícita para a ligação.

O template foi criado, mas não enviado nem submetido para aprovação de contato fora de sessão. A configuração salva não comprova a qualidade ou o funcionamento ponta a ponta de chamadas reais.

## Atualização após integração e teste

O webhook de mensagens, inicialmente preservado vazio, foi configurado em `POST https://24.199.100.186/voice/messages`. O modo live está restrito ao endereço WhatsApp do usuário de teste, conferido em uma mensagem recebida. O template nativo foi entregue e a autorização recebida acionou uma única chamada.

A ligação terminou em `completed`, duração de 14 segundos, e o usuário confirmou que ouviu e conversou com a Bia. Realtime e transcrição estão configurados. O adaptador passou a aceitar IDs de mensagem `SM` e `MM`; a primeira solicitação foi reconciliada após comprovar entrega, sem botão duplicado. Veja teste real (`voice-live-test-result.json`, evidência local não versionada) e [integração e limites](whatsapp.md).

Permanecem pendentes os templates para envio fora da janela, mídia autenticada, horários reais e homologação de reservas. Não há chamada automática pendente nem autorização para ampliar os testes a outros contatos.
