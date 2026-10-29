# Mensagens com ação clara e horário local

As mensagens mostram data e hora locais, por exemplo **29/10 às 16:30**, sem acrescentar `America/Sao_Paulo` ou outro rótulo técnico. O perfil, a API, a agenda e as regras de contato continuam guardando o fuso para converter corretamente UTC. Ocultar o rótulo não troca a hora da reunião.

Essa apresentação vale para abertura, ofertas, confirmação de reserva, mensagens da Bia, SMS, lembrete de ligação e aviso de início. O roteiro de voz recebe a hora local já convertida e a instrução de não falar códigos de fuso. As duas mensagens curtas da abertura continuam separadas.

Release de publicação: `r-2f2d835094ea`, em 29/10/2026. Os seis templates usados pela atualização foram conferidos como aprovados antes da ativação. A primeira versão do novo aviso de início foi recusada por começar com variável; o conteúdo foi corrigido com uma saudação literal e submetido como `arkom_reuniao_agora_v3`.

## Ação conforme a etapa

| Etapa | Próximo passo do cliente |
|---|---|
| Cadastro sem reserva | “Responda AGENDAR para eu te mostrar os horários…” |
| Duas vagas apresentadas | Escolher o primeiro ou o segundo horário real |
| Apenas uma vaga disponível | “Responda PRIMEIRO para escolher esse horário” |
| Reserva já confirmada no formulário e objetivo conhecido | “Você confirma sua presença?” na segunda mensagem curta |
| Reserva sem objetivo no formulário | Escolher qual rotina priorizar: atendimento, vendas ou operação |
| Preparação 15 minutos após a abertura | Enviar um exemplo da tarefa que toma tempo |
| Objeção sem motivo conhecido | Explicar o único impedimento ainda desconhecido |
| Objeção com motivo conhecido e abertura | Decidir se quer avaliar aquele processo na reunião |
| SMS de lembrete | Confirmar presença pelo WhatsApp, mantendo a instrução de saída |
| Início da reunião | “Você participa agora? Responda CONFIRMO ou REMARCAR” |

Os comandos propostos usam os caminhos existentes: AGENDAR passa pelo especialista de agenda; PRIMEIRO/SEGUNDO são escolhas verificadas pelo Go; CONFIRMO registra intenção de comparecer; REMARCAR consulta novas vagas e só troca a reserva após sucesso. A resposta “sim” à pergunta explícita de presença também é aceita. Enviar ou confirmar uma mensagem não comprova comparecimento real.

## Prompts e qualidade

`bia-specialists-v4` inclui exemplos por etapa e pede uma ação concreta depois de responder à fala comercial. No especialista de objeções, a revisão limitada identifica convite vago com “se quiser” ou ausência de próximo passo, enquanto a objeção estiver aberta e ainda não houver duas tentativas enviadas. Há no máximo uma regeneração; permanece o fallback conservador existente. Condições de reserva não confirmadas seguem o próprio esclarecimento.

O mecanismo não cola a mesma pergunta no fim de toda resposta. Agradecimento, identidade, recusa, pausa, saída e adiamento podem encerrar sem outro convite. Uma confirmação registrada não deve ser pedida novamente só para aumentar a quantidade de CTAs. A reunião, sua duração e condições comerciais continuam vindo dos fatos; o prompt não autoriza urgência fictícia, desconto ou resultado prometido.

A versão do prompt rotaciona os heads anteriores por especialista e reconstrói a continuidade com o contexto durável. Um filtro de apresentação também remove rótulos técnicos de fuso que apareçam em rascunhos ou textos antigos pendentes, sem alterar as datas nem caminhos de URLs.

## Verificação

Os testes comparam a mesma reunião UTC em Cuiabá e São Paulo: as horas locais permanecem diferentes, e nenhuma mensagem exibe o código do fuso. Também cobrem templates exatos, comandos das chamadas para ação, confirmação por “sim”, limite de texto, preservação de links e ausência de terceira tentativa de persuasão.

A matriz de conversa agora tem 60 cenários, incluindo a resposta literal AGENDAR. Nos casos comerciais selecionados, o avaliador exige uma pergunta de decisão ou ação e rejeita “se quiser”. Reconhece imperativos brasileiros como “separe” e “separa”; essa é uma checagem literal, não uma medida de conversão.

As evidências ficam em `.local/direct-response-v4-eval/`, `.local/direct-response-v4-final/` e `.local/direct-response-v4-confirm/`. A rodada de 60 cenários cobriu 97 turnos e marcou um falso positivo do avaliador (“Só separa um exemplo…” é uma ação válida). Duas respostas vagas numa repetição focal motivaram a revisão de qualidade. Outra variação válida (“Leva um exemplo…”) ampliou o reconhecimento de imperativos. Na confirmação final, nove cenários e 17 turnos passaram, sem nova falha nos critérios e sem fallback do classificador. A suíte completa com race detector e as verificações adicionais de domínio/agente/voz passaram. Nenhuma dessas conversas enviou WhatsApp ou alterou reservas de clientes. O relatório da rodada anterior continua histórico em [avaliação de 29/10](conversation-qa-2026-10-29.md).

Método de instruções e exemplos consultado: [OpenAI Docs — Prompt engineering](https://developers.openai.com/api/docs/guides/prompt-engineering).
