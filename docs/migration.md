# Migração e escopo desta etapa

Fonte: `[ARKOM] Bia SDR IA — WhatsApp Comercial.json`, 127 nós. O hash SHA-256 e o grafo de dependências estão em `workflow-inventory.json`. O arquivo original em Downloads permaneceu intacto.

Decisão de escopo do usuário: começar do zero e **não usar, por enquanto, funções do banco e serviços de agenda/ClickMax antigos**. Portanto, o resultado atual é uma implementação independente do núcleo SDR, com agenda local, não uma tradução certificada de todas as exceções do legado.

## Cobertura por responsabilidade

| Bloco legado | Implementação atual | Situação |
|---|---|---|
| Schedule, watermark, cursores | API de eventos + inbox persistente | Substituição arquitetural; polling dos canais antigos adiado |
| Preparar, Lead+Dedup, roteamento | Perfil tipado, identidade por telefone E.164, chave idempotente | Implementado; importação/mapeamento de IDs legados adiado |
| Prompt e duas chamadas Claude | OpenAI Responses com schema estrito e validação no domínio | Implementado e testado com servidor HTTP simulado; sem chamada real de IA |
| Regras de atendimento | Estado explícito, oferta, confirmação, opt-out, pausa, revisão | Implementado para cenários documentados; não equivale a todas as centenas de funções JS |
| Primeiro contato pelo formulário | Evento novo `form.submitted`, sem gate de receita | Implementado no backend; site real ainda precisa ser integrado |
| Perguntas e qualificação do site | Contrato mínimo sem site/tamanho de equipe | Implementado no backend; sem redesenho/publicação da interface |
| Agenda | Worker de agendamento e agenda transacional Go/Postgres, vagas, conflito, reserva, remarcação, cancelamento | Implementado e testado localmente |
| Agenda Cal/ClickMax | Adaptadores futuros isolados | Sem homologação; não usados pelo executável atual |
| Sessões coletivas e liberação de assento | Guardas para não cancelar a sessão inteira | Automação de transferência de assentos adiada |
| Envios DataCrazy/ClickMax | Referências de contrato + interface Messenger | Não conectados; primeiro contato depende de canal/template homologado |
| Subfluxo de áudio | SDK OpenAI Audio e download controlado | Código implementado; transcrição real depende de chave/modelo/hosts |
| CRM e alertas | Notas/tarefas idempotentes em `bia_go.crm_notes` | Implementado localmente; sincronização externa adiada |
| Follow-up | Worker próprio, fila com prazo, janela, pausa, cap e invalidação | Implementado |
| Avisos de reserva | Confirmações e lembretes ligados à reserva e ao horário | Implementado |
| Presença e recuperação | Eventos de comparecimento/falta e recuperação com consentimento | Implementado; ingestão de presença real adiada |
| Flags, logs e pós-ação | Transação de estado/outbox e resultado por operação | Implementado |
| Links curtos e clique rastreado | Sem equivalente ativo | Adiado; clique não é tratado como presença |
| Datas livres, perfis comerciais A–E, alocação por equipes, alertas VIP | Sem migração literal | Redesenho posterior com regras aprovadas e casos reais |

## O que ficou diferente de propósito

- Receita zero não leva ao silêncio nem esconde automaticamente o próximo passo.
- Primeiro contato não espera uma mensagem no WhatsApp.
- O banco novo não interpreta SQL produzido por um prompt.
- Regras determinísticas controlam reservas e envios; linguagem natural não prova que uma ação ocorreu.
- A confirmação comercial só é gerada depois de um resultado positivo do calendário.
- Uma falha de consulta não recebe o rótulo “agenda cheia”.

## Critério de substituição futura

1. Rodar replay com conversas anonimizadas e critérios comerciais aprovados.
2. Definir e implementar o canal real, templates de primeiro contato e confirmação de entrega.
3. Integrar o site, importar contatos/reservas/opt-outs e publicar capacidade real da agenda.
4. Comparar decisões em shadow, observando taxa de erro, primeiro contato e escolhas de horário.
5. Definir uma única origem responsável por cada contato. O n8n e o Go não podem enviar pela mesma identidade ao mesmo tempo.
6. Só então ativar contatos piloto e medir conversão e comparecimento. Ações antigas de shadow jamais viram envios live automaticamente.

Nada nesta entrega desativa, modifica ou publica o n8n. O deploy, a conexão dos canais e o corte de produção permanecem fora da execução desta etapa.
