---
title: Arkom — jornada do cliente, workers, agentes e follow-ups
date: 2026-10-29
tags:
  - arkom
  - sdr
  - arquitetura
  - mermaid
  - follow-up
---

# Arkom — mapa completo da Bia

Documento para importar ou colar no Obsidian. Cada bloco `mermaid` funciona de forma independente; o primeiro dá a visão de cima e os demais abrem os caminhos.

Base: implementação e configuração consultadas no servidor em **29/10/2026**, incluindo o case Camozzi, especialistas de texto com Decisions e aviso por WhatsApp no início da reunião. O mapa descreve os caminhos implementados; isso não significa que todos foram homologados em atendimento real.

**Como ler:** decisões aparecem em losangos. Estados finais de uma etapa podem voltar à conversa quando houver uma nova mensagem. Trechos explicitamente marcados como desligados, dependentes de evento ou propostos não devem ser tratados como automações ativas.

## 1. Visão de cima — jornada do cliente

```mermaid
flowchart TD
    C["Cliente chega à Arkom"] --> EN{"Como entrou?"}
    EN -->|Formulário| F["Cadastro, consentimentos e opção de horário"]
    EN -->|WhatsApp direto| W["Mensagem recebida e identidade do contato"]
    F --> FC{"Contato autorizado e elegível?"}
    FC -->|Não| BLOQ["Corrigir cadastro ou revisar bloqueio"]
    FC -->|Sim| FB{"Já existe reunião futura?"}
    FB -->|Sim| AB["Abertura confirma a reunião e aproveita o objetivo"]
    FB -->|Não| AS["Abertura convida ao agendamento"]
    W --> CT["Contexto agrupa texto, áudios e imagens"]
    AB --> VID["Após entrega da abertura: enviar case Camozzi uma vez por contato"]
    AS --> VID
    VID --> R{"Cliente responde?"}
    R -->|Sim| CT
    R -->|Não| ESP["Aguardar; não há retomada específica da abertura hoje"]
    CT --> EX["Execução escolhe agente e valida a decisão em Go"]
    EX --> INT{"Intenção e estado atual"}
    INT -->|Sem reunião| AG["Consultar vagas e oferecer até dois horários"]
    AG --> ESC{"Escolha válida?"}
    ESC -->|Ainda não| CT
    ESC -->|Sim| RES["Agenda própria grava a reserva no Postgres"]
    RES --> BK["Reunião agendada"]
    AB --> BK
    INT -->|Já tem reunião| REF["Reforçar comparecimento e esclarecer dúvidas"]
    REF --> BK
    INT -->|Quer remarcar| REM["Consultar outras vagas e trocar reserva após sucesso"]
    REM --> BK
    INT -->|Quer cancelar| CAN["Cancelar reserva e invalidar lembretes antigos"]
    INT -->|Pausa, saída ou humano| CTRL["Aplicar controle do contato"]
    BK --> CAD["Follow-up: preparação, áudio, chamada e SMS"]
    CAD --> MT["Horário da reunião"]
    MT --> OUT{"Chegou evento de presença?"}
    OUT -->|Não| SEM["Resultado desconhecido; não inferir falta"]
    OUT -->|Compareceu| ATT["Registrar comparecimento"]
    OUT -->|Faltou| NS["Registrar no-show"]
    NS --> REC["Recuperação existe no código; desligada no ambiente atual"]
    REC -.->|Se habilitada e houver resposta| CT
    CAN --> RET["Novo agendamento depende de nova iniciativa ou conversa"]
    CTRL --> RET
```

O bloco de cadência é independente de o cliente responder à primeira mensagem. **Entretanto, áudio e convite de ligação exigem uma resposta recente no WhatsApp**, além de reserva válida. O SMS usa autorização própria e não depende dessa janela.

## 2. O que está ligado hoje

| Caminho | Situação observada no servidor |
|---|---|
| Contato automático após formulário | Ativo; abertura tem espera de 10 segundos para incorporar a reserva |
| Case Camozzi logo após a abertura | Ativo no worker de mídia; uma vez por contato; template aprovado; aguarda entrega da abertura |
| Agendar no formulário e enxergar no WhatsApp | Ativo; há reserva no mesmo pedido ou em chamada posterior |
| Texto, áudio, imagem e agrupamento de mensagens | Ativos |
| Especialistas de texto com Decisions | Persuasão, objeções, agenda, comparecimento e recuperação; memória compartilhada e prompts próprios |
| Preparação 15 minutos após a abertura | Ativa, com horário permitido e validade curta |
| Áudio 30 minutos antes | Ativo, usando a gravação instalada |
| Texto e solicitação de ligação 15 minutos antes | Ativos, condicionados à janela e à permissão |
| SMS 5 minutos antes | Ativo, com `sms_consent: true` |
| WhatsApp no início | Etapa própria em T, expira em T+5; CONFIRMO ou REMARCAR; template fora da sessão |
| Botões de áudio, chamada e SMS da demonstração | Ativos; antecipam a execução sem alterar a reunião |
| Retomada 2 horas após oferta sem resposta | Implementada; desligada por `BIA_DISABLE_FOLLOWUPS=true` |
| Recuperação após no-show | Implementada; envio desligado pela mesma configuração |
| Lembretes clássicos em T−24h, T−2h e T−15min | Caminho alternativo do código; substituído pela cadência atual |
| Detectar comparecimento ou falta automaticamente | Não implementado; depende de evento informado |
| Link de reunião | Não configurado; a Bia não deve inventá-lo |
| Marcar, remarcar ou cancelar pela ligação | Não permitido; a ligação apenas consulta e registra notas |
| Nova tentativa automática depois de `busy` ou `no-answer` | Não há uma sequência de rediscagem implementada |

A agenda está configurada para reuniões de **45 minutos**, todos os dias, nas janelas **08:00–12:00 e 14:00–20:00**, com geração de 14 dias. A grade é gerada em `America/Cuiaba`; a apresentação ao cliente usa o fuso informado no cadastro. Há antecedência mínima de 30 minutos.

## 3. Formulário, identidade e primeira mensagem

```mermaid
flowchart TD
    F0["Enviar formulário"] --> F1{"Campos, telefone, consentimento e chave válidos?"}
    F1 -->|Não| FERR["HTTP 400 ou 422; corrigir o pedido"]
    F1 -->|Sim| F2{"Mesma Idempotency-Key já existe?"}
    F2 -->|Mesmo conteúdo| FDUP["Reutilizar submissão; não duplicar contato"]
    F2 -->|Conteúdo diferente| F409["HTTP 409: conflito de idempotência"]
    F2 -->|Não| F3["Salvar nome, empresa, receita, objetivo, fuso e consentimentos"]
    F3 --> F4["Resolver telefone e variação brasileira com ou sem nono dígito"]
    F4 --> F5["Reutilizar contato existente sem remover bloqueios"]
    F5 --> F6{"Veio slot_id no mesmo pedido?"}
    F6 -->|Sim| FAT["Ativação e reserva na mesma transação"]
    FAT --> F7{"Reserva aceita?"}
    F7 -->|Não| FBE["Retornar conflito ou indisponibilidade; não inventar confirmação"]
    F7 -->|Sim| F8["Publicar cadastro e evento de reserva"]
    F6 -->|Não| F8
    F8 --> F9["Enfileirar abertura para daqui a 10 segundos"]
    F9 --> F10["Se a página enviar a reserva depois, processá-la antes da abertura"]
    F10 --> F11{"Pode contatar agora?"}
    F11 -->|Pausa, saída, humano ou sem responsabilidade Go| FSTOP["Ação bloqueada ou descartada"]
    F11 -->|Abertura enviada há menos de 15 minutos| FCOOL["Descartar duplicação pelo intervalo mínimo"]
    F11 -->|Sim| F12{"Há reunião futura no estado atual?"}
    F12 -->|Não| FUN["Abertura sem reserva: convidar a escolher horário"]
    F12 -->|Sim, com objetivo| FGO["Confirmar data, hora, fuso, duração e objetivo informado"]
    F12 -->|Sim, sem objetivo| FQ["Confirmar reunião e fazer uma pergunta de preparação"]
    FUN --> FSEND["Enviar pelo WhatsApp"]
    FGO --> FSEND
    FQ --> FSEND
    FSEND --> FT{"Janela de conversa aberta?"}
    FT -->|Sim| FTXT["Enviar texto livre"]
    FT -->|Não| FTM["Usar template correspondente aprovado"]
    FTM --> FAP{"Template disponível e aprovado?"}
    FAP -->|Não| FWAIT["Adiar o mesmo job até aprovação ou expiração"]
    FAP -->|Sim| FTXT
    FTXT --> FOK["Registrar envio e horário da abertura"]
    FOK --> FPREP["Se houver reunião, avaliar preparação em mais 15 minutos"]
    FOK --> FV["Criar job de vídeo único por contato, elegível em 2 segundos"]
    FV --> FVD{"Abertura entregue ou lida?"}
    FVD -->|Ainda não| FVW["Adiar; vídeo expira após 15 minutos"]
    FVW --> FVD
    FVD -->|Falhou| FVF["Registrar falha opcional; manter conversa"]
    FVD -->|Sim| FVWIN{"Sessão recente de WhatsApp?"}
    FVWIN -->|Sim| FVS["Worker de mídia envia MP4"]
    FVWIN -->|Não| FVT{"Template de vídeo aprovado?"}
    FVT -->|Não| FVW
    FVT -->|Sim| FVS
```

**Detalhes que mudam a jornada:**

- Receita zero ou desconhecida não impede atendimento nem agendamento.
- O consentimento de SMS é separado do consentimento de contato no WhatsApp. Nova submissão sem `sms_consent: true` retira essa opção quando o perfil é atualizado.
- A espera de 10 segundos acomoda o formulário que agenda em uma segunda chamada. A reserva enviada no mesmo pedido é a opção transacional; uma segunda chamada que demore mais pode chegar depois da abertura.
- Quando a abertura já confirmou a reserva atual, a confirmação separada da agenda é descartada para não duplicar a mensagem.
- Novo formulário não remove opt-out, pausa nem atendimento humano. Repetição com nova chave também passa pelo intervalo mínimo entre aberturas.
- O vídeo não exige reserva; exige contato liberado. Não é reenviado a cada formulário, resposta ou retomada. Falhas no vídeo não colocam o WhatsApp em atendimento humano. Consulte [o contrato do vídeo](intro-video.md).
- Há um caminho legado de pareamento por link/código e uma API interna de cadastro. No servidor atual, o formulário público está em contato automático.

## 4. WhatsApp, mensagens picadas, mídia e memória

```mermaid
flowchart TD
    W0["Evento da Twilio"] --> W1{"Assinatura, número e evento válidos?"}
    W1 -->|Não| WR["Rejeitar o webhook"]
    W1 -->|Sim| W2{"Tipo de evento"}
    W2 -->|Permissão de chamada| WP["Worker de voz atualiza autorização"]
    W2 -->|Ligação recebida| WH["Encerrar: atendimento de chamada recebida não está habilitado"]
    W2 -->|Mensagem| W3["Consultar mensagem e anexos na Twilio; deduplicar"]
    W3 --> W4["Localizar contato ou criar contato de WhatsApp direto"]
    W4 --> W5["Contexto grava entrada durável no Postgres"]
    W5 --> W6["Redis acelera o agrupamento; Postgres mantém a recuperação"]
    W6 --> W7{"Há anexos no lote?"}
    W7 -->|Não| WT["Esperar 5 segundos sem novo texto"]
    W7 -->|Sim| WM["Worker de mídia prepara todos os anexos"]
    WM --> WK{"Tipo da mídia"}
    WK -->|Áudio| WA["Transcrever fala"]
    WK -->|Imagem| WI["Extrair texto e fatos com visão"]
    WK -->|PDF ou texto suportado| WD["Extrair conteúdo do documento"]
    WK -->|Tipo inválido ou falha| WF["Marcar falha; não descartar silenciosamente"]
    WA --> WS["Esperar 12 segundos entre fragmentos e 3 segundos após conclusão da mídia"]
    WI --> WS
    WD --> WS
    WF --> WS
    WT --> WN{"Chegou outro fragmento?"}
    WS --> WN
    WN -->|Sim| W6
    WN -->|Não| WPEND{"Ainda há mídia pendente?"}
    WPEND -->|Sim| WTIME["Aguardar conclusão; timeout de mídia em 120 segundos"]
    WTIME --> WPEND
    WPEND -->|Não| WFREEZE["Congelar um lote e publicar uma única Run"]
    WFREEZE --> WHIST["Anexar perfil, reserva, histórico recente e memória do agente"]
    WHIST --> WFAIL{"Lote contém falha de mídia?"}
    WFAIL -->|Sim| WHUM["Encaminhar para revisão humana"]
    WFAIL -->|Não| WEXEC["Enviar ao worker de execução"]
    WEXEC --> WRET["Devolver resultado, mensagens enviadas e response_id ao contexto"]
    WRET --> WNEXT["Próximo lote usa o estado atualizado"]
    WNEXT --> W6
```

A espera máxima normal de agrupamento é **30 segundos** desde o início do lote. Mídia ainda em processamento e a pequena espera após sua conclusão podem prolongar esse período. O lote tem limites de tamanho; excesso é separado em outra execução ou sinalizado para revisão.

O contexto usa até 20 mensagens e 24.000 bytes de histórico recente por janela, com limites adicionais para o lote. O `previous_response_id` é separado por especialidade e conversa. A referência é renovada quando ultrapassa 20 horas, 8 elos ou 16.000 tokens de entrada; o histórico recente continua disponível. Não há resumo semântico automático implementado nessa rotação.

O indicador de digitação é uma tentativa com controle de frequência, não uma confirmação de entrega. A resposta pode sair em até três partes, com pausas de 2 a 4 segundos. Se chegar nova entrada, as partes antigas ainda pendentes podem ser descartadas para evitar respostas fora de contexto.

## 5. Agentes de texto e motor de decisões

```mermaid
flowchart TD
    A0["Run com contexto consolidado"] --> ARULE{"Pode responder automaticamente?"}
    ARULE -->|Saída, humano, entrada velha ou resposta automática| ANO["Aplicar bloqueio ou ignorar execução"]
    ARULE -->|Ação explícita reconhecida pelo código| ADET["Tratar cancelamento, presença ou seleção sem depender do modelo"]
    ARULE -->|Precisa interpretar| ACAL["Consultar agenda atual"]
    ACAL --> DEC["Decisions classifica intenção, objeção, disposição, progresso e motivo"]
    DEC --> AR{"Coordenador Go aplica prioridades"}
    DEC -->|Indisponível ou inválido| FALL["Fallback conservador, sem bloquear WhatsApp por isso"]
    FALL --> AR
    AR -->|Objeção ou condição pendente| AOBJ["Especialista de objeções"]
    AR -->|Pedido operacional de agenda| ASCH["Especialista de agendamento"]
    AR -->|Reserva futura e dúvida sobre a conversa| AATT["Especialista de comparecimento"]
    AR -->|Nova conversa após no-show| AREC["Especialista de recuperação"]
    AR -->|Valor e interesse inicial| APER["Especialista de persuasão"]
    AOBJ --> AP["Prompt próprio mais identidade comum da Bia e memória compartilhada"]
    ASCH --> AP
    AATT --> AP
    AREC --> AP
    APER --> AP
    AP --> AM["Responses propõe decisão com ações limitadas à especialidade"]
    AM --> AQ["Revisão de qualidade: no máximo uma regeneração"]
    AQ --> AV["Motor Go valida estado, evidência e ação"]
    ADET --> AV
    AV --> AA{"Ação validada"}
    AA -->|reply| AT["Responder dúvida ou acolher; sem perguntas repetidas"]
    AA -->|offer_slots| AO["Oferecer até duas vagas reais ou registrar pendência"]
    AA -->|book ou reschedule| AB["Publicar ordem para o worker de agenda"]
    AA -->|cancel| AX["Exigir pedido explícito e executar cancelamento"]
    AA -->|confirm| AF{"Presença foi confirmada explicitamente?"}
    AF -->|Sim| AYES["Gravar intenção de comparecer; não comprova presença real"]
    AF -->|Não| ASAFE["Converter em resposta; não bloquear nem alterar presença"]
    AA -->|pause| APA["Pausar lembretes"]
    AA -->|opt_out| AST["Bloquear novos contatos"]
    AA -->|handoff| AH["Ativar atendimento humano e registrar motivo"]
    AT --> AOUT["Preparar efeitos e retorno ao contexto"]
    AO --> AOUT
    AYES --> AOUT
    ASAFE --> AOUT
    AOUT --> FRESH{"Nova entrada durante a Run interpretada?"}
    FRESH -->|Sim| DROP["Descartar avaliação e efeitos antigos; liberar próxima Run"]
    FRESH -->|Não| PUBLISH["Enviar como Bia; registrar argumentos efetivamente enviados"]
    PUBLISH --> A0
```

| Especialidade atual | Quando entra | Objetivo |
|---|---|---|
| `persuasion` | Interesse inicial ou dúvida de valor sem barreira concreta | Relacionar o objetivo à reunião e facilitar o próximo passo |
| `objections` | Barreira de adequação, tempo, investimento, confiança, sócio ou formato | Esclarecer o motivo e tratar a barreira sem repetir argumentos |
| `scheduling` | Intenção de consultar vagas, escolher ou alterar reserva | Transformar escolha clara em ordem de agenda validada |
| `attendance` | Reserva futura, preparação e dúvidas sobre a reunião | Sustentar comparecimento e preservar a reserva atual |
| `recovery` | Estado `no_show` informado | Acolher a ausência e facilitar nova escolha, se houver conversa |
| Voz de lembrete | Chamada autorizada vinculada a reunião existente | Lembrar, consultar dados e registrar nota; prompt próprio |
| Extração de mídia | Anexo recebido | Produzir conteúdo de referência; não toma ações comerciais |

Os cinco especialistas têm **prompts próprios**, com uma especialidade escolhida por Run e identidade comum da Bia. Decisions faz a leitura, o Go escolhe a rota e Responses escreve a resposta. Histórico e memória comercial são compartilhados; os response IDs são separados e versionados por especialista. Ter o envio de recuperação desligado não impede selecionar `recovery` numa nova conversa de quem tem no-show informado. Consulte [contratos e limites](specialists.md).

Se já há reunião futura, o atendimento prioriza comparecimento e dúvidas. Sem reunião, objetivo conhecido e aceite bastam para oferecer horários. Se o objetivo estiver ausente, o prompt permite uma pergunta curta; não exige site, equipe ou nova rodada de faturamento. Pedido explícito de remarcação abre novamente a consulta de vagas.

## 6. Agenda — escolha, reserva, remarcação e cancelamento

```mermaid
flowchart TD
    G0["Cliente quer escolher ou alterar horário"] --> G1{"Origem da escolha"}
    G1 -->|Formulário| GF["Consultar disponibilidade e enviar slot_id"]
    G1 -->|WhatsApp| GW["Consultar agenda para esta Run"]
    GW --> GV{"Consulta de vagas"}
    GV -->|Falhou| GE["Informar falha de consulta e registrar pendência"]
    GV -->|Sem vagas| GN["Informar ausência de vagas e registrar pendência"]
    GV -->|Tem vagas| GO["Oferecer até duas opções reais no fuso do cliente"]
    GO --> GC{"Resposta escolhe uma opção sem ambiguidade?"}
    GC -->|Não| GCL["Esclarecer a escolha; não criar reserva"]
    GCL --> GO
    GC -->|Sim| GS["Validar opção oferecida, antecedência e responsável"]
    GF --> GS
    GS --> GD{"Pedido repetido?"}
    GD -->|Mesmo pedido já concluído| GEX["Retornar resultado existente"]
    GD -->|Chave com outros dados| GCF["Conflito de idempotência"]
    GD -->|Novo| GT["Bloquear identidade e vaga em transação"]
    GT --> GP{"Vaga ainda disponível e contato elegível?"}
    GP -->|Não| GER["Retornar conflito; recarregar vagas ou repetir se transitório"]
    GP -->|Sim| GB{"Já existe uma reserva?"}
    GB -->|Não| GNEW["Criar reserva e ocupar vaga"]
    GB -->|Sim, pedido de remarcação| GR["Trocar reserva mantendo provedor e responsável"]
    GB -->|Sim, tentativa de outra reserva pelo formulário| GAL["Retornar reserva existente via consulta; impedir duplicação"]
    GNEW --> GCOM["Confirmar transação e publicar evento de agenda"]
    GR --> GCOM
    GCOM --> GSTATE["Atualizar contexto, confirmar horário e planejar cadência"]
    GSTATE --> GINV["Lembretes da reserva antiga ficam inválidos"]
    GX["Pedido explícito de cancelamento no WhatsApp"] --> GXC["Agenda cancela e libera a vaga"]
    GXC --> GCLR["Limpar reserva atual, invalidar lembretes e confirmar cancelamento"]
    GE --> GWAIT["Pendência sem reserva inventada"]
    GN --> GWAIT
    GWAIT --> GRETURN["Nova consulta depende de nova conversa ou ação operacional"]
```

No formulário público, uma tentativa de novo agendamento com reserva ativa é rejeitada; o endpoint `/test-status` permite recuperar a reserva atual. A remarcação conversacional usa a reserva antiga como parte da operação. A aplicação não deve anunciar uma alteração antes do sucesso da agenda. A reserva antiga é preservada se a transação de remarcação não concluir.

Há proteções adicionais para contextos de reuniões em grupo e troca de responsável/provedor. A configuração atual é individual e usa a agenda local; situações incompatíveis vão para revisão humana. A existência de adaptadores antigos no repositório não significa que n8n, Cal ou ClickMax participem desta configuração.

## 7. Todos os follow-ups e seus relógios

**T0** = envio bem-sucedido da abertura do formulário. **T** = início real da reunião na reserva atual. Preparação depois de T0 e lembretes antes de T são relógios diferentes.

```mermaid
flowchart TD
    P0["Cadastro e conversa"] --> P1{"Existe reunião futura?"}
    P0 --> PCASE["Entrega da abertura mais 2 segundos<br/>Case Camozzi único; expira em 15 minutos"]
    P1 -->|Não| PNO["Atendimento busca agendamento"]
    PNO --> PO{"Horários já foram oferecidos?"}
    PO -->|Não e cliente em silêncio| PSIL["Aguardar; sem sequência própria de retomada da abertura"]
    PO -->|Sim| P2H["Retomada em mais 2 horas<br/>IMPLEMENTADA, DESLIGADA"]
    P2H -.-> PR["Se habilitada: perguntar se quer seguir; resposta volta à conversa"]
    P1 -->|Sim| PBOOK["Agenda grava ou atualiza a reserva"]
    PBOOK --> PSCHED["Scheduler reconcilia etapas a cada 30 segundos"]
    PSCHED --> PT0["T0 mais 15 minutos<br/>Preparação por WhatsApp"]
    PSCHED --> PT30["T menos 30 minutos<br/>Áudio pronto pelo worker de mídia"]
    PSCHED --> PT15["T menos 15 minutos<br/>Texto e fluxo de autorização da chamada"]
    PSCHED --> PT5["T menos 5 minutos<br/>SMS transacional"]
    PSCHED --> PTNOW["T: início da reunião<br/>WhatsApp com resposta direta"]
    PTNOW --> PCNOW{"Contato liberado e dentro de T a T mais 5 minutos?"}
    PCNOW -->|Não| PSKNOW["Pular aviso expirado ou bloqueado"]
    PCNOW -->|Sim| PWIN{"Sessão recente de WhatsApp?"}
    PWIN -->|Sim| PN["Enviar aviso com CONFIRMO ou REMARCAR"]
    PWIN -->|Não| PAPP{"Template de início aprovado?"}
    PAPP -->|Sim| PN
    PAPP -->|Não| PWAIT["Adiar o mesmo job até aprovação ou expiração"]
    PN --> PREPLY["Resposta entra na mesma conversa; reunião segue relevante até o fim"]
    PT0 --> PC0{"Ainda antes de T menos 30, dentro da validade e do horário permitido?"}
    PC0 -->|Sim| PREP["Pedir exemplo de tarefa para preparar a reunião"]
    PC0 -->|Não| PSK0["Pular preparação; não enviar atrasada"]
    PT30 --> PC30{"Sessão recente de WhatsApp, áudio pronto e contato liberado?"}
    PC30 -->|Sim| PAUD["Enviar a gravação instalada"]
    PC30 -->|Não| PSK30["Pular ou bloquear conforme o motivo"]
    PT15 --> PC15{"Sessão recente e contato liberado?"}
    PC15 -->|Sim| PCALL["Entrar no fluxo de permissão e ligação"]
    PC15 -->|Não| PSK15["Não iniciar chamada por esse caminho"]
    PT5 --> PC5{"Autorizou SMS e há celular válido?"}
    PC5 -->|Sim| PSMS["Enviar SMS; não exige janela de WhatsApp"]
    PC5 -->|Não| PSK5["Não enviar SMS"]
    PBOOK --> POLD["Lembretes clássicos T menos 24h, 2h e 15min<br/>ALTERNATIVA INATIVA nesta configuração"]
    PE["Evento explícito de no-show"] --> PREC["Recuperação por WhatsApp<br/>IMPLEMENTADA, ENVIO DESLIGADO"]
    PREC -.-> PR
```

| Etapa | Quando fica elegível | Janela e condições principais | Conteúdo e responsável |
|---|---|---|---|
| Abertura | Cerca de 10s após ativar formulário público | Cadastro elegível; template aprovado fora da sessão; validade de 24h; intervalo mínimo entre aberturas de 15min | Execução envia texto/template montado com perfil e reserva |
| Vídeo Camozzi | Elegível 2s após o envio da abertura | Aguarda entrega da abertura; vence em 15min; contato liberado; template de vídeo fora da sessão | Mídia envia o MP4 instalado, uma vez por contato |
| Confirmação de reserva | Após sucesso da agenda | Antes do início da reunião; aguarda janela de WhatsApp se não foi coberta pela abertura | Worker de agenda; não é uma nova geração livre de horários |
| Preparação | T0 + 15min | Só se há reunião; vence após 5min ou em T−30, o que ocorrer primeiro; respeita horário permitido | Follow-up envia texto/template de preparação, sem depoimento |
| Áudio | T−30min | Vence em T−25; sessão recente de WhatsApp, consentimento e arquivo pronto; horário permitido | Follow-up encaminha; mídia envia a gravação existente |
| Convite/chamada | T−15min | Texto e início do pedido de permissão até T−10; chamada depende de permissão e reunião ainda futura | Follow-up envia texto; voz trata autorização e chamada |
| WhatsApp no início | T | Expira em T+5 ou no fim da reserva; contato liberado; template aprovado fora da sessão | Follow-up envia texto determinístico com CONFIRMO ou REMARCAR |
| SMS | T−5min | Até T; autorização específica, celular válido e canal configurado; não depende de sessão nem do horário comercial | Follow-up envia texto determinístico por Twilio SMS |
| Retomada de oferta | Oferta + 2h, ajustada ao horário permitido | Desligada hoje; quando ligada, requer a mesma oferta atual, nenhuma reserva e limite de uma retomada em 24h | Follow-up envia texto de retomada; não é uma sequência diária infinita |
| Lembretes clássicos | T−24h, T−2h, T−15min | Alternativa quando a cadência de reunião está desligada; não roda em paralelo com ela | Follow-up de texto; também afetado pelo bloqueio de retomadas gerais |
| Recuperação de falta | Evento de no-show válido | Envio desligado hoje; se habilitado, passa pelo próximo horário permitido e por checagens da reserva e da sessão | Follow-up envia uma recuperação; nova mensagem seleciona `recovery` |

**Regras que interrompem ou mudam a cadência:**

- Cancelar, remarcar, registrar presença/ausência, pedir pausa, sair ou transferir para humano pode invalidar ações pendentes. A reserva e o contato são relidos antes do envio.
- Os IDs das etapas incluem reserva e horário. Reconciliação e reinício não recriam automaticamente uma etapa já concluída ou pulada. Uma remarcação cria outra identidade de cadência.
- O horário normal de contato é 08:00–20:00 no fuso do cliente. Se o próximo horário permitido estiver depois da validade da etapa, ela é pulada. O SMS T−5 tem regra própria e pode sair, por exemplo, às 07:55 para reunião às 08:00.
- A aplicação usa uma margem conservadora de **23h desde a última entrada** para a sessão de WhatsApp. Abertura e preparação têm templates próprios para fora dessa sessão. Áudio não dispõe desse mesmo caminho de template; a permissão de chamada fora de sessão não está habilitada.
- Confirmar intenção de comparecer não cancela os lembretes. Não existe hoje uma ramificação de cadência diferente para quem confirmou e quem não confirmou.
- O áudio é um arquivo pronto; o worker não gera uma versão personalizada para cada pessoa.
- Como não há link de sala configurado, a confirmação não contém link fictício. O texto atual do SMS também não inclui link de sala.

## 8. Ligação — autorização e tentativa

```mermaid
flowchart TD
    V0["Lembrete T menos 15 ou botão de demonstração"] --> VTXT["Enviar texto de convite no WhatsApp"]
    VTXT --> VOK{"Texto teve envio confirmado?"}
    VOK -->|Não| VSTOP["Não criar solicitação dependente; registrar falha"]
    VOK -->|Sim| VREQ["Criar solicitação de voz vinculada à reserva"]
    VREQ --> VGUARD{"Reserva futura e contato autorizado, sem pausa ou humano?"}
    VGUARD -->|Não| VSKIP["Encerrar solicitação sem ligar"]
    VGUARD -->|Sim| VPERM{"Há autorização permanente registrada?"}
    VPERM -->|Sim| VREADY["Solicitação pronta para uma chamada"]
    VPERM -->|Não| VLIM{"Pode pedir autorização dentro da sessão e dos limites?"}
    VLIM -->|Não| VB["Bloquear ou expirar com motivo"]
    VLIM -->|Sim| VBUT["Solicitar botão nativo VOICE_CALL_REQUEST"]
    VBUT --> VDEL["Consultar resultado final da mensagem na Twilio"]
    VDEL --> VR{"Resultado"}
    VR -->|Erro 37010: permissão permanente já existe| VSAVE["Registrar autorização permanente"]
    VSAVE --> VREADY
    VR -->|Falha de entrega comum| VFAIL["Registrar falha; não presumir autorização"]
    VR -->|Botão entregue| VANS{"Resposta do cliente"}
    VANS -->|Aceita| VACC["Gravar permissão temporária ou permanente"]
    VACC --> VREADY
    VANS -->|Rejeita ou revoga| VREJ["Bloquear esta autorização e chamadas dependentes"]
    VANS -->|Não responde| VWAIT["Aguardar até expiração; não ligar"]
    VREADY --> VCURRENT["Reconsultar contato e worker de agenda"]
    VCURRENT --> VDIAL["Twilio inicia a chamada no WhatsApp"]
    VDIAL --> VCON{"Resultado da tentativa"}
    VCON -->|Atendida| VAI["Abrir áudio em tempo real e conversa da Bia"]
    VCON -->|busy ou no-answer| VNO["Registrar duração e estado; sem rediscagem automática"]
    VCON -->|Falha confirmada| VE["Registrar erro do provedor"]
    VCON -->|Resultado incerto| VUNC["Marcar uncertain; revisar antes de repetir"]
```

Uma solicitação produz no máximo uma chamada automática. O aceite temporário dura sete dias; a autorização permanente não recebe essa expiração local, mas pode ser revogada. Os pedidos de permissão têm limite local de uma solicitação em 24h e duas em sete dias quando um novo pedido é necessário.

Receber `37010` não significa que o botão ainda precisa chegar: o provedor informou que a autorização permanente já existe. A aplicação então segue com a solicitação atual. Pedido entregue, sozinho, não equivale a autorização.

Enquanto há chamada ativa ou uma operação de voz incerta, respostas e efeitos de outros canais podem aguardar para evitar duas abordagens simultâneas. As mensagens recebidas continuam sendo armazenadas. Pedidos explícitos de saída têm caminho prioritário no motor, e a sessão relê as condições para interromper o atendimento.

## 9. Dentro da chamada — prompt e ferramentas próprios

```mermaid
flowchart TD
    L0["Chamada atendida e sessão autorizada"] --> LGET["Consultar reunião e briefing no worker de agenda"]
    LGET --> LVALID{"Agenda ainda disponível e reserva correta?"}
    LVALID -->|Não| LEND["Encerrar sem inventar dados"]
    LVALID -->|Sim| LOPEN["Cumprimentar, confirmar identidade e lembrar data e hora"]
    LOPEN --> LWHO{"É a pessoa certa?"}
    LWHO -->|Não| LWRONG["Registrar nota de número incorreto e encerrar"]
    LWHO -->|Sim| LASK["Perguntar se a reunião está de pé"]
    LASK --> LRESP{"Resposta"}
    LRESP -->|Confirma| LCONF["Registrar nota confirmed"]
    LRESP -->|Quer remarcar| LREM["Registrar nota reschedule_request"]
    LRESP -->|Quer cancelar| LCAN["Registrar nota cancel_request"]
    LRESP -->|Tem dúvida| LQ["Responder com dados disponíveis; anotar pergunta se necessário"]
    LRESP -->|Quer parar contato| LOUT["Aplicar pedido de saída e encerrar"]
    LCONF --> LNOTE{"Agenda confirmou gravação da nota?"}
    LREM --> LNOTE
    LCAN --> LNOTE
    LQ --> LNOTE
    LNOTE -->|Sim| LACK["Pode dizer que anotou; não prometer alteração ou notificação"]
    LNOTE -->|Não| LNACK["Não afirmar que registrou ou avisou alguém"]
    LACK --> LBYE["Despedida e end_call"]
    LNACK --> LBYE
    LOPEN -->|Caixa postal| LVM["voicemail_detection: lembrete curto e encerramento"]
    LOPEN -->|Silêncio ou dificuldade persistente| LSIL["Tentativas curtas conforme roteiro; encerrar"]
    LBYE --> LPLAY["Aguardar reprodução da despedida e desligar"]
    LPLAY --> LHIST["Enviar transcrições e fala reproduzida ao contexto"]
    LVM --> LHIST
    LSIL --> LHIST
```

Ferramentas atuais: `get_meeting`, `record_reminder_note`, `end_call` e `voicemail_detection`. As notas aceitam confirmação, pedido de remarcação, pedido de cancelamento, pergunta e número incorreto.

**Pedido anotado na ligação não modifica a reserva nem aciona automaticamente uma conversa de texto.** A equipe consulta a nota, ou o cliente continua pelo WhatsApp para uma alteração executável. Uma confirmação verbal gravada em nota também não é o evento de comparecimento real.

O áudio passa por Twilio Media Streams e OpenAI Realtime, com interrupção da fala quando o cliente fala. O contexto recebe a fala da Bia depois da confirmação de reprodução; uma fala interrompida não é registrada como integralmente ouvida. O limite da sessão é de cinco minutos. Dados vazios de consultor, formato, sala ou briefing continuam desconhecidos.

## 10. Silêncio, pausa, saída, humano e resultado da reunião

```mermaid
flowchart TD
    S0["Cliente está no atendimento"] --> SE{"Evento ou comportamento"}
    SE -->|Silêncio sem reunião e sem oferta| SSW["Aguardar; sem sequência específica ativa"]
    SE -->|Silêncio após oferta| SOFF["Retomada mais 2h existe, mas está desligada"]
    SE -->|Silêncio com reunião| SBOOK["Cadência continua se os requisitos de cada canal permitirem"]
    SE -->|Vou pensar ou aviso depois| STHINK["Responder brevemente e respeitar decisão"]
    SE -->|Pedido de pausa| SPAUSE["Pausar lembretes; a conversa pode continuar"]
    SPAUSE --> SRES{"Evento operacional cadence.resume?"}
    SRES -->|Sim e sem opt-out| SBACK["Liberar cadência elegível; não recriar etapas vencidas"]
    SRES -->|Não| SHOLD["Manter pausa dos lembretes"]
    SE -->|Pedido explícito para parar| SOPT["Opted-out e pausado; não enviar novos contatos"]
    SOPT --> SNOFORM["Novo formulário ou mensagem não remove automaticamente o bloqueio"]
    SE -->|Pedido de humano ou condição de revisão| SHUM["Human true; interromper ações automáticas"]
    SHUM --> SHREL{"Evento human.release?"}
    SHREL -->|Não| SHWAIT["Aguardar atendimento ou revisão"]
    SHREL -->|Sim| SAUTO["Voltar à automação; preservar pausa e saída se existirem"]
    SBOOK --> ST["Chega o horário da reunião"]
    ST --> SAT{"Evento externo de resultado foi informado?"}
    SAT -->|Não| SUNK["Resultado permanece desconhecido"]
    SAT -->|attendance.attended| SDONE["Registrar comparecimento e encerrar esta cadência"]
    SAT -->|attendance.no_show| SNS["Registrar falta e invalidar lembretes pré-reunião"]
    SNS --> SRC["Recuperação de falta: envio desligado hoje"]
    SRC -.->|Se habilitada e o cliente responder| SNEW["Agente recovery oferece novo caminho de agenda"]
    SNEW --> SCYCLE["Reserva nova inicia nova cadência"]
```

Não há detecção automática de entrada na sala, reunião realizada, venda ou falta. Passar do horário, não atender uma chamada ou não responder WhatsApp não cria `attendance.no_show`. Os resultados devem chegar por integração ou ação operacional.

O fluxo atual também não contém uma cadência comercial de pós-reunião, proposta, fechamento ou pós-venda. Esses são possíveis próximos ciclos, não caminhos já ativos.

## 11. Antes de qualquer ação — bloqueios, concorrência e falhas

```mermaid
flowchart TD
    E0["Uma ação ficou pronta na fila"] --> E1{"Modo shadow?"}
    E1 -->|Sim| ESIM["Registrar simulação; nenhuma ação externa"]
    E1 -->|Não| E2{"Go é responsável pelo contato?"}
    E2 -->|Não| ESKIP["Descartar ou bloquear com motivo"]
    E2 -->|Sim| E3{"Consentimento, controle, reserva e prazo continuam válidos?"}
    E3 -->|Não| ESKIP
    E3 -->|Sim| E4{"Há entrada nova ou chamada em andamento?"}
    E4 -->|Entrada nova| EN["Priorizar contexto novo; adiar lembrete ou descartar resposta antiga"]
    E4 -->|Chamada ativa ou incerta| EV["Aguardar término ou revisão da chamada"]
    E4 -->|Não| E5{"Horário e limites permitem executar?"}
    E5 -->|Pode aguardar dentro da validade| EWAIT["Adiar o mesmo job; preservar identidade"]
    E5 -->|Prazo ou limite definitivo vencido| ESKIP
    E5 -->|Sim| ELEDGER["Registrar tentativa antes da operação externa"]
    ELEDGER --> EOP["Executar no canal ou na agenda"]
    EOP --> ER{"Resultado observado"}
    ER -->|Sucesso| EDONE["Salvar resultado e publicar retorno ao contexto"]
    ER -->|Pré-requisito ainda pendente| EWAIT
    ER -->|Falha transitória segura para repetir| ERETRY["Repetir com limite e espera progressiva"]
    ER -->|Resultado externo incerto| EUNC["Marcar uncertain; não repetir a mutação cegamente"]
    ER -->|Falha definitiva| EDEAD["Marcar dead ou failed e registrar motivo"]
    EUNC --> EREV["Revisão da tentativa e do provedor"]
    EDEAD --> EREV
    EREV --> ECHAN{"Qual operação falhou?"}
    ECHAN -->|SMS ou vídeo opcional| ESMS["Registrar revisão sem tomar o atendimento do WhatsApp"]
    ECHAN -->|Envio de texto, áudio ou outra ação crítica| EHUM["Pode ativar revisão humana e bloquear automação"]
    ECHAN -->|Voz| EVO["Estado próprio de voz; uncertain pode segurar outros envios"]
```

Antes de criar um job, os endpoints também podem devolver rejeições: pedido inválido, conflito de idempotência, submissão expirada, contato bloqueado, reserva ausente, sessão de WhatsApp ausente, consentimento de SMS ausente, voz em andamento e limites de demonstração. **HTTP 202 significa que a ação entrou na fila; não significa entrega.**

Os estados `sent` e `delivered` também são diferentes. Os consumidores podem concluir um envio quando o provedor informa `sent`. O resultado final pode exigir consulta à Twilio; não há hoje reconciliação completa e contínua de todos os envios antigos. A auditoria recente confirmou entregas consultando o provedor diretamente.

As rejeições HTTP anteriores ao job ainda não possuem uma trilha persistente por tentativa. Isso é uma lacuna de observabilidade, não um caminho de cliente resolvido por um prompt.

## 12. Botões da demonstração — antecipar sem esperar a reunião

```mermaid
flowchart TD
    D0["Mesmo formulário de teste"] --> DSTAT["Consultar test-status do telefone e submission_id"]
    DSTAT --> DGUARD{"Contato liberado, submissão ativa e reunião futura?"}
    DGUARD -->|Não| DERR["Mostrar motivo; corrigir estado antes de repetir"]
    DGUARD -->|Sim| DVOICE{"Há solicitação de voz pendente, ativa ou incerta?"}
    DVOICE -->|Sim| DWAIT["voice_request_in_progress; consultar a solicitação existente"]
    DVOICE -->|Não| DT{"Botão escolhido"}
    DT -->|30 minutos| DA["POST reminders/audio<br/>Exige áudio pronto e resposta recente no WhatsApp"]
    DT -->|15 minutos| DC["POST reminders/call<br/>Exige voz habilitada e resposta recente no WhatsApp"]
    DT -->|5 minutos| DS["POST reminders/sms<br/>Exige autorização SMS e celular válido"]
    DA --> DID["Preservar Idempotency-Key e conteúdo nas repetições"]
    DC --> DID
    DS --> DID
    DID --> DQ["HTTP 202 e job manual nos mesmos workers"]
    DQ --> DRES["Consultar status_url até resultado da etapa"]
    DRES --> DOUT{"Estado final"}
    DOUT -->|Concluído| DOK["Conferir recebimento no aparelho e resultado do provedor"]
    DOUT -->|Falhou, bloqueado ou incerto| DREVIEW["Usar código e chave para revisão; não criar UUIDs para contornar limite"]
```

Esses botões antecipam o envio atual. Eles não mudam data/hora, não consomem automaticamente a etapa futura da cadência e não desativam a etapa automática equivalente. Portanto, um áudio manual agora e o T−30 futuro podem produzir dois envios intencionais.

Validade da demonstração: até 10 minutos, limitada ao início da reunião. Há intervalo mínimo por tipo de disparo, limite global e limites próprios da autorização de chamada. O botão não ignora consentimento, reserva, controle humano, sessão de WhatsApp ou autorização de voz.

Base dos endpoints: `/webhooks/test-form/:key`.

| Endpoint | Função |
|---|---|
| `POST /` | Cadastrar formulário e iniciar contato; aceita `slot_id` opcional |
| `GET /availability` | Consultar vagas livres |
| `POST /bookings` | Reservar pelo formulário já ativado |
| `POST /test-status` | Consultar reserva, consentimentos e pré-requisitos do contato |
| `POST /sms-consent` | Registrar ou retirar a preferência SMS; aplicação é assíncrona |
| `POST /reminders/audio` | Antecipar áudio |
| `POST /reminders/call` | Antecipar convite e fluxo de chamada |
| `POST /reminders/sms` | Antecipar SMS |
| `GET /reminders/:action/:id` | Consultar as etapas da solicitação manual |

## 13. Visão técnica — os seis workers e a comunicação

```mermaid
flowchart LR
    CLIENT["Cliente e formulário web"] --> API["API de cadastro, agenda e demonstração"]
    CLIENT <--> TW["Twilio<br/>WhatsApp, chamada e SMS"]
    TW --> WEB["Webhooks assinados"]
    API <--> PG[("PostgreSQL<br/>Perfil, reserva, filas, histórico, resultados e notas")]
    WEB --> CTX["Worker de contexto"]
    CTX <--> PG
    CTX <--> REDIS[("Redis<br/>Buffer e prazo rápido de agrupamento")]
    CTX --> MEDIA["Worker de mídia<br/>Transcrição, visão, gravação e case em vídeo"]
    MEDIA --> CTX
    MEDIA <--> PG
    CTX --> EXEC["Worker de execução<br/>Roteador e motor de decisões"]
    EXEC <--> PG
    EXEC <--> TEXTAI["OpenAI Responses<br/>Especialidade de texto selecionada"]
    MEDIA <--> MEDIAAI["OpenAI<br/>Transcrição e extração de mídia"]
    EXEC --> SCHED["Worker de agenda<br/>Eventos, reservas e caixa de consultas"]
    SCHED <--> PG
    PG --> FOLLOW["Worker de follow-up<br/>Scheduler e consumidor de cadências"]
    FOLLOW --> MEDIA
    FOLLOW --> VOICE["Worker de voz<br/>Permissão, tentativa e sessão"]
    FOLLOW --> TW
    EXEC --> TW
    MEDIA --> TW
    VOICE <--> TW
    VOICE <--> REALTIME["OpenAI Realtime<br/>Prompt de lembrete"]
    VOICE <-->|Consulta e nota via mailbox no Postgres| SCHED
    VOICE --> CTX
```

As setas entre workers representam trabalho e resultados persistidos, não necessariamente uma chamada HTTP direta. As filas e caixas de entrada são do Postgres (`jobs`, `worker_tasks`, `agenda_requests`, `voice_requests`). Redis acelera a janela, mas não é a única cópia das mensagens.

A reserva pelo formulário é uma exceção importante à frase “só o worker de agenda grava reservas”: **a API também pode reservar em transação**, reutilizando as regras da agenda local e publicando o evento que o worker de agenda processa. Já as decisões de agenda geradas na conversa são executadas pelo worker de agenda.

Todo o controle comercial e a orquestração estão no nosso código Go. Twilio transporta mensagens/chamadas, OpenAI executa inferência e Postgres/Redis mantêm estado. O n8n não é uma etapa desse caminho.

### Estados principais para enxergar o cliente

| Dimensão | Estados ou informações que influenciam o caminho |
|---|---|
| Identidade e controle | Telefone, responsável `go`, consentimento, `paused`, `opted_out`, `human` |
| Perfil compartilhado | Pessoa, empresa, receita, objetivo e fuso |
| Reserva | Sem reserva, agendada, em andamento pelo intervalo, remarcada, cancelada, resultado desconhecido, compareceu ou faltou |
| Oferta | Sem oferta, duas opções atuais, escolha pendente, agenda vazia ou consulta indisponível |
| Contexto | Fragmentos chegando, mídia pendente, Run congelada, resposta em partes ou entrada nova |
| Memória | Histórico compartilhado, objeção/motivo, argumentos enviados, condição pendente e `previous_response_id` por especialista/versão |
| Canal | Sessão de WhatsApp, consentimento e telefone SMS, permissão de chamada temporária/permanente |
| Operação | Pendente, executando, adiada, concluída, pulada, falhou ou incerta |

## 14. Ciclo proposto para evoluir prompts e agentes

**Este diagrama é uma proposta de trabalho, não uma automação já implantada.** O ciclo atual de atendimento está nas seções anteriores. Aqui está o ciclo para melhorá-lo com evidência e sem deixar o agente publicar sozinho suas próprias regras.

```mermaid
flowchart LR
    I0["Conversas, resultados de agenda e entrega dos canais"] --> I1["Classificar desfecho e ponto de atrito"]
    I1 --> I2{"Qual é a causa?"}
    I2 -->|Regra, estado, fila ou integração| IC["Corrigir código ou configuração; criar regressão"]
    I2 -->|Condução, intenção ou redação| IP["Revisar prompt e exemplos da especialidade"]
    I2 -->|Falta um papel ou uma passagem| IA["Desenhar responsabilidade e contrato de novo agente"]
    IC --> CASES["Casos de avaliação com contexto e resultado esperado"]
    IP --> CASES
    IA --> CASES
    CASES --> OFF["Reexecutar casos gravados e testes sem contato real"]
    OFF --> REVIEW["Revisão humana de fatos, agenda, tom e efeitos propostos"]
    REVIEW --> PASS{"Versão aprovada?"}
    PASS -->|Não| I2
    PASS -->|Sim| VER["Versionar prompt, regra, modelo e contrato"]
    VER --> DEMO["Teste controlado no formulário e nos canais"]
    DEMO --> MET["Medir agendamento, comparecimento, bloqueios e custo"]
    MET --> GOOD{"Melhorou sem regressão?"}
    GOOD -->|Sim| DEPLOY["Promover versão e acompanhar"]
    GOOD -->|Não| ROLL["Reverter versão e revisar causa"]
    DEPLOY --> I0
    ROLL --> I2
```

### Fronteiras sugeridas para discutir novos agentes

| Responsabilidade | Como está hoje | Próxima evolução para avaliar |
|---|---|---|
| Agendamento inicial | `persuasion` e `scheduling`, com prompts próprios e Decisions | Avaliar conversas e comparar agendamento real |
| Comparecimento | Especialista `attendance`; cadência determinística | Medir presença e qualidade das respostas de preparação |
| Objeções | Especialista próprio, memória de motivos e argumentos enviados | Ampliar biblioteca com casos reais e medir resolução reconhecida pelo cliente |
| Retomada sem resposta | Uma retomada de oferta existe, mas está desligada; não cobre toda abertura | Projetar uma cadência finita, com saída explícita, intervalo e critérios por estágio |
| Reagendamento pedido na voz | Apenas nota para consulta | Passagem durável para texto/agenda e confirmação do cliente antes de alterar a reserva |
| Falta à reunião | Evento externo e especialidade `recovery` | Integrar presença real e definir uma sequência curta de recuperação |
| Depois da reunião | Não há cadência comercial própria | Definir proposta, decisão, fechamento e pós-venda somente após existir resultado confiável |
| Qualidade e supervisão | Testes, logs e consultas manuais | Avaliador de conversas que proponha melhorias para revisão, sem alterar produção sozinho |

### Perguntas que o mapa permite decidir

1. Qual é o próximo passo para quem preencheu, não agendou e nunca respondeu à abertura?
2. Como oferecer uma saída clara a quem está pensando, sem transformá-la em pausa permanente indevida?
3. Preparação noturna deve ser pulada, remarcada para a manhã ou não ser programada nesse horário?
4. Quem pediu remarcação na voz deve receber uma conversa de texto automaticamente? Qual confirmação é necessária?
5. O que fazer após `busy` ou `no-answer`: nenhuma nova tentativa, uma retomada por texto ou uma tentativa em outro momento?
6. Qual integração será a fonte confiável de comparecimento e no-show?
7. Antes de ligar a recuperação, qual texto substituirá a referência à URL de agendamento que hoje está vazia?
8. O que muda na cadência para alguém que confirmou presença, demonstrou dúvida ou pediu ajuda para entrar?
9. Quem recebe e assume o atendimento quando `human=true`? Hoje há estado e nota local; não existe notificação automática de equipe implementada por esse caminho.
10. Qual identificador e trilha de requisições permitem descobrir um erro de formulário sem depender de uma captura de tela?

## 15. Limites atuais que não devem desaparecer no desenho

- Não há ciclo automático de autoedição de prompts nem conversa livre entre agentes. Roteamento e validação são do código.
- O estado de atendimento humano exige liberação operacional. Uma nova saudação ou um novo formulário não fazem essa liberação.
- Pedidos de pausa precisam de `cadence.resume` para reabrir a cadência. Voltar a conversar, sozinho, não corresponde a esse evento.
- Ausência de vagas gera pendência; não há notificação automática implementada para quando uma vaga nova surgir.
- A chamada registra notas e não executa alteração de reserva. Essas notas não notificam automaticamente o consultor nem iniciam um agente de texto.
- Não há rediscagem automática nem detector automático de falta. `busy`, `no-answer`, silêncio e mensagem não lida não comprovam no-show.
- O envio de recuperação de falta está desligado e seu texto atual usa a URL de agendamento. Como essa URL está vazia neste ambiente, ele precisa ser revisado antes de habilitar esse caminho.
- A antecedência mínima é 30 minutos, mas o mapa não promete que rede, fila e operadora entreguem exatamente no segundo programado.
- A cadência manual e a automática são independentes. É possível receber ambas no mesmo teste.
- Dizer que não foram encontradas novas falhas na auditoria não equivale a homologar todos os caminhos deste documento.

## 16. Referências do código e da configuração

| Parte do mapa | Fonte principal no repositório |
|---|---|
| Entrada, cadastro e primeira abertura | `internal/httpapi/test_form.go`, `internal/store/form_contact.go`, `internal/domain/form_contact.go` |
| Reserva pelo formulário | `internal/httpapi/test_calendar.go`, `internal/store/form_booking.go` |
| Entrada WhatsApp e permissão | `internal/voiceworker/messages.go`, `internal/voiceworker/http.go` |
| Contexto, agrupamento e memória | `internal/store/context.go`, `internal/contextworker/worker.go`, `internal/contextworker/buffer.go` |
| Extração de mídia | `internal/providers/media.go`, `internal/providers/audio.go`, `internal/mediaworker/worker.go` |
| Vídeo após a abertura | `internal/domain/intro_video.go`, `internal/providers/video.go`, `internal/casemedia/asset.go` |
| Decisions, especialistas e prompts de texto | `internal/agent/decisions.go`, `internal/agent/specialists.go`, `internal/agent/prompts/`, `internal/agent/openai.go` |
| Memória comercial e revisão | `internal/domain/dialogue.go`, `internal/agent/quality.go` |
| Decisões, controle e validações | `internal/domain/engine.go`, `internal/worker/worker.go` |
| Operações e consulta da agenda | `internal/schedulingworker/worker.go`, `internal/schedulingworker/agenda.go`, `internal/providers/local.go` |
| Cadência e reconciliação | `internal/domain/reminders.go`, `internal/domain/sms.go`, `internal/store/reminders.go`, `internal/followupworker/worker.go` |
| Voz, permissão e ferramentas | `internal/voiceworker/worker.go`, `internal/voiceworker/store.go`, `internal/voiceworker/tools.go` |
| Roteiro e áudio da chamada | `internal/voiceworker/reminder_prompt.txt`, `internal/voiceworker/prompt.go`, `internal/voiceworker/bridge.go` |
| Botões e pré-requisitos | `internal/httpapi/test_reminders.go`, `internal/store/test_reminders.go`, `internal/httpapi/test_contact.go` |
| Resposta em partes | `internal/domain/reply_parts.go`, `internal/worker/reply_typing.go` |
| Parâmetros | `internal/config/config.go`, `internal/config/voice.go`; valores não secretos conferidos no servidor |

Valores observados: `live`, contato automático e vídeo de abertura ligados, retomadas gerais desligadas, preparação/reunião/SMS/voz ligados, sem restrição a um único telefone de teste, sem URL de sala, sem URL de agendamento e sem pedido de permissão de voz fora da sessão. Os limites atuais de homologação são 100 envios por contato/dia e 300 globais/hora; não são uma recomendação de cadência comercial.
