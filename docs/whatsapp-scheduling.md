# Atendimento e agenda própria

O prompt de WhatsApp está em `internal/agent/whatsapp_prompt.txt`. Ele é independente de `internal/voiceworker/reminder_prompt.txt`, reservado à ligação de lembrete. A referência do n8n foi adaptada ao calendário próprio e ao contrato de decisões do Go; regras antigas de receita, ClickMax, coletivas e promessas de contato humano não são executadas.

O objetivo informado no formulário já basta para avançar. Ao aceitar continuar ou explicar uma necessidade, o agente reconhece a mensagem e solicita `offer_slots`. Sem objetivo, cabe no máximo uma pergunta de contexto. As vagas reais e seus rótulos de data/fuso entram no contexto a cada execução. O modelo pode escolher dois IDs válidos conforme a preferência do lead; o Go monta os horários visíveis, sem expor IDs técnicos.

O estilo incorpora a revisão comercial v2: respostas diretas, uma ideia por bolha, menos repetição de nome, elogios e emojis. Dúvidas de preço, tempo e adequação recebem resposta antes da oferta. “Vou pensar” e “vou olhar e te aviso” recebem espaço para decidir, sem pergunta ou insistência. A resposta sobre ser IA é transparente e considera se já existe reserva. Condições de gratuidade, bônus ou entrega não são ampliadas por uma reformulação comercial. Sem link na reserva, não há promessa de convite ou envio posterior.

Essa revisão modifica o atendimento do WhatsApp. Uma normalização de texto remove aberturas automáticas e um “ok” depois do aviso de reserva recebe uma resposta breve, sem virar outra confirmação de presença. Respostas a perguntas explícitas de presença continuam válidas. Mantém o modelo, o ritmo de agrupamento/envio e o roteiro de ligação existentes. A naturalidade é avaliada junto com a correção da ação: uma resposta simpática que deixa de oferecer horários quando deveria avançar não atende ao objetivo.

Respostas como “o primeiro”, “a segunda opção”, “às 14h” ou um dia com opção única são validadas contra os horários oferecidos. “Sim” para duas opções, negação e escolhas ambíguas não reservam. A reserva e a confirmação pertencem ao worker de agenda, em transação PostgreSQL; uma proposta da IA nunca confirma uma reunião por si só.

Agenda vazia e falha na consulta têm mensagens diferentes, sem link inexistente nem novo questionário. A pendência é registrada no CRM local. Não há promessa de retorno automático nesse estado.

## Disponibilidade diária

Configuração autorizada para homologação: todos os dias, incluindo fins de semana, 08h–12h e 14h–20h, fuso `America/Cuiaba`, duração de 45 minutos, uma reunião simultânea. Grade iniciada às 08h e às 14h: a última reunião da manhã termina às 11h45 e a última da tarde às 20h. Mantida antecedência mínima de 30 minutos.

```dotenv
BIA_LOCAL_CALENDAR_ENABLED=true
BIA_LOCAL_CALENDAR_TIMEZONE=America/Cuiaba
BIA_LOCAL_CALENDAR_WINDOWS=08:00-12:00,14:00-20:00
BIA_LOCAL_CALENDAR_DURATION_MINUTES=45
BIA_LOCAL_CALENDAR_DAYS=14
BIA_LOCAL_CALENDAR_CLOSER_ID=1
BIA_LOCAL_CALENDAR_MEETING_URL=
```

O worker de agenda mantém uma janela móvel de 14 dias e a recompõe no início do processo e a cada 15 minutos. A operação é idempotente e preserva reservas e vagas manuais. A consulta retorna até 100 vagas livres próximas. Mudanças na configuração não apagam capacidade já publicada; retirar ou alterar horários existentes exige conciliação explícita para preservar reservas.

Por decisão do usuário, a homologação grava somente no banco, com `meeting_url` vazio. A confirmação informa dia/hora, sem link. Não cria Google Meet nem convite de calendário externo. Links vazios são permitidos apenas para reservas do provedor local. Os lembretes de áudio/ligação continuam sujeitos à configuração separada de follow-ups.

## Validação

Testes de domínio verificam escolha inequívoca, datas/fuso, preferência validada, agenda vazia e reserva sem link. O teste PostgreSQL percorre criação idempotente de vagas → oferta → escolha natural → worker de agenda → reserva → confirmação, conferindo que nova geração de vagas não libera a reserva. Também há avaliação real do modelo com dados sintéticos, sem mensagens ou chamadas para clientes.

O schema estruturado é validado na aplicação antes de executar efeitos, seguindo a separação entre decisão e execução descrita na [documentação oficial da OpenAI](https://developers.openai.com/api/docs/guides/function-calling).
