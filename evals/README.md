# Avaliação das conversas da Bia

`conversations.json` contém somente pessoas e situações fictícias. A avaliação chama os mesmos especialistas, Decisions e regras de domínio do serviço. Recebe apenas `OPENAI_API_KEY` e `OPENAI_MODEL`; não instancia mensageiro, banco, mídia, voz ou follow-up. Reservas e cancelamentos são simulados em memória, depois das mesmas verificações de escolha explícita do domínio.

Executar da raiz, com a chave já salva no `.env` local:

```sh
python3 scripts/eval-conversations.py -repeat 2 -workers 3
```

Isso usa a API OpenAI e consome tokens. O modelo de geração é o configurado, e o classificador é `gpt-6-luna`, como na homologação. A chave não é escrita nos resultados nem passada como argumento do processo. Variáveis já definidas no ambiente têm prioridade sobre `.env`.

Para repetir apenas casos específicos:

```sh
python3 scripts/eval-conversations.py -case conditional_free,reschedule,withdraw_reschedule -repeat 2 -out .local/conversation-eval-targeted
```

## Resultados e critérios

Cada execução salva `turns.jsonl` e `summary.json` em `.local/conversation-eval/`, fora do Git. Contêm fala do cliente fictício, resposta efetivamente simulada, decisões, especialista, diagnóstico do classificador, uso de head anterior, duração e falhas. Um erro devolve exit code 1. O runner não repete silenciosamente um teste falho até conseguir aprovação.

- A avaliação observa efeitos e estado, incluindo escolhas ambíguas bloqueadas pelo domínio, em vez de tratar toda proposta do modelo como uma ação concluída.
- Cada cenário mantém contexto, memória comercial, argumentos enviados e `previous_response_id` por especialista ao longo dos turnos. A primeira resposta após reserva simulada já recebe a nova reserva.
- Testa condições pendentes, recusa, pausa, saída, humano, objetivos já informados, horário/fuso e reserva existente. Expectativas de especialista são exigidas nos casos de troca relevantes.
- Frases proibidas e fatos esperados são verificações literais por caso; não substituem revisão de significado. A suíte também limita a uma pergunta por resposta.
- Atendimento iniciado pelo cliente pode responder sem consentimento de formulário. Isso não autoriza campanhas ou lembretes; um opt-out existente continua impedindo a resposta automática.
- Um pedido de humano verifica ativação do controle humano/registro interno. Não testa disponibilidade de atendentes nem promete transferência imediata.

Testes de integração separados usam apenas `localhost/arkom_bia_test` para verificar reservas reais em Postgres, corrida por vaga, remarcação atômica, memória e novas mensagens durante uma Run. O runner de API não comprova esses efeitos em banco e não testa o transporte WhatsApp.

A matriz é finita e os modelos podem variar. Aprovar os casos não prova ausência de todos os erros possíveis, naturalidade perfeita ou melhora de conversão. Revise os diálogos e acrescente falhas observadas em atendimento real à matriz. Referência de método: [OpenAI Docs: Evaluation best practices](https://developers.openai.com/api/docs/guides/evaluation-best-practices).
