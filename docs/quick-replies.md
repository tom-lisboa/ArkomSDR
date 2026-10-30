# Botões nativos do WhatsApp

O Go escolhe o próximo passo pela etapa e envia o Content da Twilio com botões de resposta rápida. O clique chega no webhook `/voice/messages` como `ButtonPayload` e segue pelo mesmo worker de contexto e pela mesma conversa. O usuário também pode responder por texto ou áudio.

| Momento | Botões |
|---|---|
| Retomadas sem resposta, em 15min/2h/dia seguinte/3 dias | Ver horários · Tirar dúvida · Agora não |
| Retomada sem resposta, em 6h | Ver horários · Como funciona · Agora não |
| Retomada sem resposta, em 48h | Quero conversar · Tenho uma dúvida · Agora não |
| WhatsApp T−24h, T−2h, T−1h, T−5min e ajuda T+7min | Confirmar presença · Remarcar · Preciso de ajuda |
| Texto T−15, antes da permissão nativa de ligação | Confirmar presença · Remarcar · Preciso de ajuda |
| Cadastro sem reserva | Ver horários · Tirar dúvida |
| Cadastro com reserva e objetivo informado, na segunda mensagem curta | Confirmar presença · Remarcar · Tirar dúvida |
| Cadastro com reserva sem objetivo | Atendimento · Vendas · Operação |
| Preparação, 15 minutos depois da abertura | Atendimento · Vendas · Operação |
| Duas opções reais da agenda | Primeiro horário · Segundo horário · Outros horários |
| Uma opção real | Escolher horário · Outros horários · Tirar dúvida |
| Confirmação da reserva e lembretes de texto, dentro da janela do WhatsApp | Confirmar presença · Remarcar · Preciso de ajuda |
| Início da reunião | Confirmar presença · Remarcar · Preciso de ajuda |
| Retomada e recuperação já existentes | Ver horários · Tirar dúvida |

As respostas de conversa recebem um menu quando existe uma pergunta de decisão. As opções de horário aparecem junto das datas exatas e na última mensagem da resposta picada. Depois de confirmar a presença, o menu comum deixa apenas Remarcar e Tirar dúvida. Os botões de lembrete não autorizam ligações: a permissão nativa de chamadas continua separada.

## Ação e contexto

Cada payload contém a ação, validade e assinatura HMAC com uma chave derivada logicamente do `BIA_API_TOKEN` pelo domínio de assinatura `arkom-whatsapp-button-v1`. O token não contém o segredo. Ele está vinculado ao contato, à criação do cadastro e à reserva atual. Primeiro/Segundo também ficam vinculados à versão da oferta e às vagas apresentadas; expiram em 20 minutos. Os demais expiram em 24 horas. Rotacionar o token de API invalida os botões anteriores.

A assinatura do webhook, a identidade do destinatário e o Message SID continuam verificados na Twilio. O worker de execução revalida o payload sob o lease do contato, antes de consultar o modelo. Um clique de agenda consulta a disponibilidade atual; selecionar uma opção passa pela mesma verificação determinística de reserva, inclusive condições pendentes. Remarcar mantém a reserva anterior até a nova reserva ter sucesso. A confirmação só registra intenção de comparecimento.

Cliques repetidos do mesmo Message SID não geram outra execução. Botões de outra pessoa, adulterados, expirados ou de uma reserva/oferta anterior não alteram a agenda: recebem opções para continuar. Uma janela com clique e outros fragmentos pede uma escolha nova, sem assumir confirmação. Um pedido explícito de saída tem prioridade. Os tokens não são enviados ao modelo; a conversa recebe a intenção correspondente ao botão.

## Publicação e janela do WhatsApp

`deploy/quick-replies/` tem oito recursos Content. Os três `session-*` aceitam corpo e títulos dinâmicos e são usados **somente** na janela de atendimento aberta por mensagem do usuário. Permanecem sem submissão à Meta. Para evitar o erro `21656`, o corpo variável de sessão normaliza quebras de linha e espaços repetidos; os parágrafos literais dos templates aprovados continuam preservados. Os cinco de abertura/contexto/preparação/início têm corpo específico e aprovação para contato iniciado pela empresa.

`BIA_QUICK_REPLIES_ENABLED=true` exige os oito SIDs listados em `.env.example`. Com a flag desligada, o transporte usa os templates de texto anteriores. Antes de ativar, conferir o conteúdo exato, as variáveis e o status aprovado dos cinco templates iniciados pela empresa. Nunca usar o recurso genérico de sessão para contornar essa aprovação.

A implementação segue [twilio/quick-reply](https://www.twilio.com/docs/content/twilio-quick-reply). Os botões da imagem “Entrar agora” e “Entrar na reunião” são botões de URL, conforme [twilio/call-to-action](https://www.twilio.com/docs/content/twilio-call-to-action). Dependem de uma sala real e de outro Content aprovado. Ainda não há sala configurada neste projeto: o recurso de entrar na sala permanece pendente. “Remarcar” aqui abre a conversa com a agenda em Go; não redireciona para um site.

A ampliação de 30/10 inclui ajuda em T+7, apenas sem interação/confirmacão recente e com a reunião ainda em andamento. Essa mensagem não afirma ausência. Recuperação por falta continua dependendo do evento explícito `attendance.no_show`; o relógio, a entrega de WhatsApp e um clique de confirmação não comprovam comparecimento ou falta.

## Verificação

Os testes incluem entrega com ContentSid e tokens dentro/fora da janela, bloqueio de template pendente, idempotência de envio e clique, webhook assinado, continuidade do contato existente, confirmação sem modelo, confirmação antiga contra reserva nova, escolha de vaga exata, condições pendentes, lote de mensagens em ambas as ordens, saída prioritária e preservação da oferta junto dos botões. A integração usa PostgreSQL local isolado e uma Twilio simulada; não dispara mensagens para clientes.

Publicação em 29/10/2026: release `r-cf673f61b678`. Os cinco recursos de contato iniciado pela empresa foram aprovados antes da ativação; os três de sessão permaneceram sem submissão. A suíte completa com `-race` e PostgreSQL isolado passou; a checagem adicional de variáveis passou após a normalização de espaços. `go vet ./...` passou. A verificação operacional conferiu os sete serviços na mesma imagem, a flag e os oito SIDs em cada processo, prontidão da API e saúde de voz. Evidência operacional local: `.local/deploy/quick-replies-live-verification.json`. A renderização final dos botões no aparelho ainda depende do teste de WhatsApp.
