# Atendimento e agenda própria

O prompt de WhatsApp está em `internal/agent/whatsapp_prompt.txt`. Ele é independente de `internal/voiceworker/reminder_prompt.txt`, reservado à ligação de lembrete. A referência do n8n foi adaptada ao calendário próprio e ao contrato de decisões do Go; regras antigas de receita, ClickMax, coletivas e promessas de contato humano não são executadas.

O objetivo informado no formulário já basta para avançar. Ao aceitar continuar ou explicar uma necessidade, o agente reconhece a mensagem e solicita `offer_slots`. Sem objetivo, cabe no máximo uma pergunta de contexto. As vagas reais e seus rótulos de data/fuso entram no contexto a cada execução. O modelo pode escolher dois IDs válidos conforme a preferência do lead; o Go monta os horários visíveis, sem expor IDs técnicos.

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
