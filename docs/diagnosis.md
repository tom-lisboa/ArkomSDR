# Diagnóstico e critérios do novo funil

Base: relato do usuário, quatro imagens e exportação do fluxo de 127 nós. Não foram feitas consultas à produção.

## Evidência e limites

| Evidência | Interpretação possível | Limite |
|---|---|---|
| Usuário sem faturamento não viu a agenda; ao informar faturamento passou a vê-la | Há condicionamento do caminho de agendamento ao perfil | A regra exata do formulário não está no JSON |
| Site mostra CTA “Agendar Consultoria de IA” | O botão existe; o gargalo relatado vem depois do clique | A observação inicial de ausência do CTA foi corrigida |
| Formulário aparece na etapa 4 de 9 | A jornada possui várias etapas | As nove perguntas completas não foram fornecidas |
| Página mostra “agenda cheia”, calendário e nenhum horário | O lead termina sem um horário selecionável | Não prova agenda realmente lotada; pode haver falha, configuração, filtro ou janela de consulta |
| Formulário promete resultado/convite no WhatsApp; usuário não recebeu contato | Existe quebra da expectativa de primeiro atendimento | Sem logs não é possível atribuir a falha a um provedor específico |
| Fluxo reage às conversas e seu prompt diz que fala quando o lead escreve | Esse fluxo não constitui, por si só, um gatilho confiável de primeiro contato ao cadastrar | Pode existir outro fluxo responsável por iniciar contato, que não foi exportado |
| Site informa 45 minutos; biblioteca legada usa 60 | Existe divergência de duração entre as fontes fornecidas | Pode refletir migração de agenda, não necessariamente o mesmo tipo de evento |

O JSON contém comentários e instruções de operação do sistema antigo. Foram tratados como evidência técnica, não como ordens para publicar, alterar credenciais, manter regras antigas ou acessar produção.

## Regras novas

1. Cadastro aceito → persistência e primeiro contato enfileirado no mesmo processamento, sem depender de receita ou mensagem prévia.
2. Uma conversa adequada para receita zero continua existindo. Não oferecer exclusivamente um grupo nem presumir falta de interesse.
3. Captura mínima: nome, WhatsApp e consentimento para contato. E-mail, objetivo e receita são dados opcionais; site e tamanho da equipe ficam fora.
4. Horários reais precisam ser publicados. Código não cria capacidade comercial inexistente.
5. `available`, `empty` e `unavailable` têm significados diferentes. Preferência de dia/período é registrada e gera tarefa local; não há promessa de contato automático sem uma rotina implementada.
6. Presença confirmada, ausência e cancelamento são estados distintos. “Não posso nesse horário” exige esclarecimento ou remarcação, não cancelamento automático.
7. Remarcar mantém o especialista; disponibilidade insuficiente vira revisão. Nunca perder a reserva anterior por uma tentativa falha.
8. Pedidos de parada prevalecem sobre lembretes e novas tentativas.

## Medição

Separar perdas antes da reserva de faltas após uma reserva. Receita, origem e perfil devem ser dimensões de análise, não motivos para silêncio.

- Cadastro → contato aceito pelo provedor: p50/p95 e percentual dentro do SLA. Meta inicial proposta: até 60 segundos; não é resultado comprovado.
- Cadastro → visualização da agenda → preferência/seleção → reserva.
- Taxa de respostas `empty` e `unavailable`, separadas.
- Reservas → confirmação de presença → comparecimento.
- No-show: faltas verificadas / (faltas verificadas + presenças verificadas). Reservas futuras e presença desconhecida não entram no denominador.
- Resultado de recuperação: novo horário marcado e comparecimento posterior.

`/v1/metrics` já mostra fila, ações simuladas, envios aceitos, operações incertas, falhas e p95 do primeiro contato. Os contadores de presença atuais representam o **último estado de cada lead**, não uma análise histórica por reunião. Um dashboard completo de conversão exige instrumentação do site e um modelo analítico por reserva.

## Cenários prioritários

Receita zero, perfil sem receita, formulário repetido, agenda vazia, consulta com erro, vagas concorrentes, pedido de parada, atraso sem cancelamento, troca de horário mantendo closer, novo evento invalidando lembrete antigo, falha de transcrição, resposta incompleta do modelo, envio sem confirmação e correção posterior de presença.
