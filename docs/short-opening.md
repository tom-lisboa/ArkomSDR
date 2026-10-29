# Abertura em mensagens curtas

A confirmação do cadastro com reunião passa a chegar em duas mensagens distintas, com uma pausa de dois segundos programada na fila. A segunda só é criada após o comprovante de envio da primeira. Não depende de o cliente responder para ser enviada: fora da janela do WhatsApp, cada parte usa seu próprio template aprovado.

Publicado em 29/10/2026, release `r-34b0b1c935d6`. Os quatro templates foram conferidos como `approved` na Twilio antes da ativação. A suíte completa com `go test -race ./... -count=1` e `go vet ./...` passou; o novo formato ainda precisa de validação visual no aparelho.

Exemplo com objetivo conhecido:

> Oi, Ana! Sou a Bia, IA da Arkom. Sua reunião está confirmada para 30/10 às 14:00. Separe 45 minutos para essa conversa.

> Na reunião, vamos avaliar onde a IA pode ajudar. Seu foco: melhorar o atendimento.
>
> Você confirma sua presença?

Sem objetivo informado, a segunda mensagem pergunta qual rotina priorizar: atendimento, vendas ou operação. Quem ainda não tem reunião recebe somente a abertura curta com a ação de responder AGENDAR para ver horários. [Direct response e apresentação do horário local](direct-response.md).

O objetivo completo fica no perfil do banco e no contexto dos especialistas. Apenas o trecho mostrado na abertura é limitado a 90 caracteres. O primeiro nome exibido fica limitado a 20 caracteres. Essas medidas reduzem o texto; o aplicativo WhatsApp controla a exibição de “Ler mais/More”, que pode variar por aparelho e tamanho de fonte.

## Templates e envio

| Campo do ambiente | Conteúdo |
|---|---|
| `TWILIO_FORM_CONTACT_CONTENT_SID` | Abertura curta de quem não agendou |
| `TWILIO_BOOKED_CONTACT_CONTENT_SID` | Confirmação curta de reserva |
| `TWILIO_BOOKED_QUESTION_CONTENT_SID` | Mesmo template de confirmação; mantido por compatibilidade |
| `TWILIO_FORM_CONTEXT_CONTENT_SID` | Segunda mensagem com objetivo conhecido |
| `TWILIO_FORM_CONTEXT_QUESTION_CONTENT_SID` | Segunda mensagem sem objetivo conhecido |

O adapter confere aprovação antes de cada envio por template. Dentro da sessão, envia as mesmas partes como texto. Aprovação pendente adia a mesma parte, sem reenvio da confirmação concluída. A segunda expira em 15 minutos ou no início da reunião, o que ocorrer primeiro.

Não se aplica o divisor de respostas livres ao template: cada mensagem deve corresponder integralmente ao conteúdo aprovado. Fonte: [Twilio — Session Definitions](https://www.twilio.com/docs/content/session-definitions).

## Ordem e interrupções

- A confirmação é gerada da reserva atual no momento do envio, incluindo horário local e duração reais; o fuso é usado internamente na conversão.
- A segunda parte usa `form_context` na fila de execução, com ID derivado da primeira; a criação é atômica com a conclusão da primeira parte.
- Cada parte tem recibo e idempotência próprios. Reiniciar os serviços não reinicia a abertura.
- Nova mensagem do cliente, cancelamento, remarcação, saída, pausa, controle humano ou retirada de consentimento impedem a continuação antiga. O atendimento segue pelo contexto normal.
- O vídeo aguarda enquanto a segunda mensagem estiver pendente. Depois de enviada ou descartada, segue as verificações existentes. Uma indisponibilidade de envio da segunda parte não coloca o atendimento inteiro em controle humano.
- O timer de preparação de 15 minutos continua contando da primeira mensagem enviada. O aviso de reserva separado continua sendo suprimido quando já foi coberto pela abertura.
- Cadastros que já receberam a abertura não ganham uma segunda mensagem retroativa.

## Verificação

Os testes verificam correspondência exata entre texto e JSON do template, limites das bolhas com valores longos, preservação do objetivo completo e ausência de pergunta repetida. A integração usa PostgreSQL isolado e provedor simulado: duas mensagens com ContentSid, aprovação pendente da segunda, ordem antes do vídeo, execução única e interrupções por novo contexto ou controle do contato. Não envia mensagens reais a participantes.
