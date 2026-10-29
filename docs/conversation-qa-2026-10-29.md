# Avaliação da conversa da Bia — 29/10/2026

A matriz final de **59 cenários foi executada duas vezes: 190 turnos sem falhas nos critérios automáticos**. Uma rodada complementar de **20 cenários com reserva, também repetidos, somou 66 turnos sem falhas**. Essa segunda rodada já excluiu o intervalo ocupado da disponibilidade e incluiu no histórico a confirmação de cancelamento emitida pelo worker de agenda. Total das duas rodadas finais: **256 turnos**, sem mensagens reais ou dados de clientes.

As correções estão publicadas na homologação, release `r-4938fd387153`, com prompt `bia-specialists-v3`. Geração: `gpt-5.4-mini`; classificação: `gpt-6-luna`. O teste chama os mesmos prompts e código de planejamento/domínio da aplicação. Mídias e follow-ups não foram objeto desta avaliação.

A conferência após publicação confirmou `/readyz` e a saúde do serviço de voz com HTTP 200, além dos sete serviços na mesma release, ativos e sem reinícios. O ambiente de homologação continha um cadastro, um formulário, uma conversa e uma reserva; a avaliação sintética não criou esses registros e não os removeu.

## Cobertura

| Caminho | Verificações |
|---|---|
| Primeiro atendimento e agendamento | Intenção de marcar, aceite do convite, receita zero, objetivo conhecido, ausência de objetivo e ausência de nova qualificação desnecessária |
| Escolha de vaga | Primeiro/segundo, horário literal, resposta ambígua, duas opções na mesma fala, negação e expressão de cortesia |
| Condições pendentes | Gratuidade/duração não confirmadas, manutenção da condição no turno seguinte e retirada explícita antes da reserva |
| Disponibilidade | Agenda vazia, erro de consulta, manhã, depois das 17h e horário solicitado sem vaga |
| Objeções | Adequação, empresa pequena, uso prévio de ChatGPT, investimento, tempo, confiança e dependência de sócio |
| Continuidade comercial | Motivo já explicado, objeção após oferta, troca do motivo, retomada da agenda após resolução e limite de insistência |
| Reserva pelo formulário | Horário/duração, objetivo, preparação, ausência de link, agradecimento e objeção de quem já tem reunião |
| Confirmação | Confirmação explícita, “sim” em resposta à pergunta de presença e agradecimento sem nova confirmação |
| Remarcação | Oferta antes da escolha, nova seleção, indisponibilidade preservando reserva e desistência da troca mantendo o original |
| Cancelamento | Pedido direto, linguagem natural, pergunta/negação sem cancelamento e retorno à agenda após cancelar |
| Preferências e controle | Pausa, saída, contato já bloqueado, humano solicitado e atendimento iniciado pelo próprio cliente sem formulário |
| Intervalo e resultado | Reunião em andamento, atraso, remarcação no início, falta registrada e reunião anterior sem dedução automática de no-show |
| Identidade e contexto | Bia identifica-se como IA, mensagens agrupadas, fuso, entrada automática e tentativa de transformar texto do cliente em instrução interna |

A lista completa e as falas ficam em [`evals/conversations.json`](../evals/conversations.json). Os critérios verificam efeitos e estado após o domínio, além de algumas exigências de especialista, fatos e trechos proibidos. Uma proposta de reserva do modelo não é contabilizada como reserva quando a escolha literal é rejeitada pelo Go.

## Problemas encontrados e corrigidos

| Problema observado | Correção |
|---|---|
| Uma vaga válida à noite era completada com outra de manhã | Subconjunto validado não ganha uma segunda vaga fora da preferência; limites literais filtram as vagas antes da oferta |
| Classificação inconsistente de aceite condicionado | Condição explícita força esclarecimento; fica persistida, inclusive quando a categoria é formato ou desconhecida; o domínio também bloqueia reserva prematura |
| Retirar a condição não liberava a escolha | Retirada explícita e escolha literal retornam à agenda; “não precisa ser gratuito” não é confundido com negar a vaga |
| Pedido de remarcação podia receber “qual prefere?” sem opções | Rota de remarcação oferece vagas reais antes da escolha |
| Desistência da remarcação deixava vagas antigas selecionáveis | Pedido de manter o original limpa as opções e preserva a reserva |
| “Pode desmarcar meu horário de amanhã” exigia outra confirmação | Padrões literais de cancelamento natural seguem a agenda; pergunta, negação e comando embutido em instruções não autorizam cancelamento |
| Pedido de humano era devolvido como nova pergunta da Bia | Intenção classificada como humano ativa handoff; pausa explícita também não depende da concordância do redator |
| Agenda vazia gerava promessa de avisar depois sem uma ação programada | Pedido de vagas sem disponibilidade usa a resposta do domínio e registra a necessidade; não inventa acompanhamento |
| Diagnóstico interno longo derrubava uma decisão válida | Metadados são limitados preservando UTF-8; campos operacionais continuam validados |
| Limpeza de abertura produzia fragmentos como “O receio.” | Remoção automática exige pontuação/abertura isolada; preserva o verbo de uma frase completa |
| Introdução do modelo repetia “tenho estes horários” | Cabeçalho redundante é removido antes da oferta gerada pelo domínio |
| Cliente voltava da objeção, mas classificação sem barreira mantinha a rota anterior | Intenção de objeção sem categoria nem causa é normalizada; quem tem reserva retorna ao apoio de comparecimento |

A primeira rodada levantou dez sinais de falha. Um era uma expectativa incorreta do avaliador: uma resposta ao cliente que iniciou o contato pode ser entregue sem consentimento de formulário. Isso não autoriza campanha ou lembrete. Outro sinal envolvia escolha ambígua que o domínio já bloqueava com segurança; o avaliador passou a observar o efeito real. Os demais levaram aos reparos acima, junto com os casos adicionais de remarcação e apresentação do texto.

## Evidências das rodadas finais

| Rodada | Cenários distintos | Repetições | Turnos | Falhas nos critérios | Fallback do classificador | Turnos com head anterior |
|---|---:|---:|---:|---:|---:|---:|
| Matriz completa | 59 | 2 | 190 | 0 | 1 | 41 |
| Complemento com reserva | 20, já incluídos nos 59 | 2 | 66 | 0 | 0 | 14 |

O fallback ocorreu em uma resposta com escolha ambígua das duas vagas. A saída pediu esclarecer a opção; não reservou nem interrompeu a conversa. O teste não identifica a causa externa específica da indisponibilidade do classificador.

Os arquivos locais de evidência, fora do Git, são:

- `.local/conversation-eval-release/{turns.jsonl,summary.json}`;
- `.local/conversation-eval-bookings-final/{turns.jsonl,summary.json}`;
- `.local/conversation-eval-go-tests.log`;
- `.local/deploy/conversation-qa-live-verification.jsonl`.

Também passaram os testes de agente/domínio com `-race`, a suíte de contexto, a seleção de integrações de conversa/agenda em **`localhost/arkom_bia_test`**, `go vet ./...` e a compilação Linux. As integrações verificam persistência de memória, isolamento/versionamento dos heads, descarte após nova entrada, escolha natural, disputa por vaga e reserva/remarcação/cancelamento pelo worker responsável. Não usaram a base de homologação.

## Repetir a avaliação

```sh
python3 scripts/eval-conversations.py -repeat 2 -workers 3
```

Configuração, filtros, resultados e fronteiras do runner: [`evals/README.md`](../evals/README.md). A execução consome tokens da API OpenAI. Não executa transporte WhatsApp, agenda externa, mídia ou disparos da cadência.

## Limites

Este resultado aprova uma matriz finita; não significa que todas as conversas possíveis estão livres de erro. As respostas são variáveis. Verificações literais e de estado não medem integralmente naturalidade, persuasão, conversão ou percepção de troca de especialista. Houve revisão manual das transcrições principais, mas não julgamento semântico independente de cada resposta.

O runner simula o calendário em memória; a confirmação de cancelamento é espelhada do worker e não executada por ele nessa rodada de API. Os testes de integração são a evidência separada de efeitos no banco. A extensão final que permite manter o horário original entre fragmentos separados por quebra de linha passou por regressão local; as rodadas de API usaram o mesmo pedido pontuado. A experiência no WhatsApp continua sendo uma validação complementar.

Método consultado: [OpenAI Docs — Evaluation best practices](https://developers.openai.com/api/docs/guides/evaluation-best-practices).
