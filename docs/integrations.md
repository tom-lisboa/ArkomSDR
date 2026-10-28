# Contratos e operação

## Entrada HTTP

Todos os caminhos `/v1` exigem `Authorization: Bearer <BIA_API_TOKEN>`. São contratos internos entre backends. O site público deve autenticar/proteger sua própria entrada e encaminhar ao serviço; não existe verificação de assinatura de Meta/ClickMax diretamente nestes endpoints.

`POST /v1/forms`: corpo em `examples/form.json`, com `Idempotency-Key` obrigatório. A chave repetida com o mesmo conteúdo não duplica a execução; conteúdo diferente retorna 409. O telefone é normalizado para E.164 antes da identidade estável do lead.

`POST /v1/events`:

```json
{
  "id": "message-provider-unique-id",
  "kind": "message.received",
  "lead_id": "lead_id_retornado_pelo_formulario",
  "at": "2026-10-27T14:00:00-04:00",
  "text": "Quero ver os horários"
}
```

Tipos: `form.submitted`, `message.received`, `schedule.preference`, `booking.created`, `booking.rescheduled`, `booking.cancelled`, `attendance.attended`, `attendance.no_show`, `contact.opt_out`, `human.takeover`, `human.release`, `cadence.resume`, `ownership.changed`.

Campos adicionais: `preference` para preferência; `booking` para reserva criada/remarcada; `booking_id` para cancelamento/presença; `owner` (`go`, `n8n`, `shadow`) para propriedade. `selected_slot_id` representa uma escolha explícita no site. Mensagens recebidas precisam de texto, seleção ou `audio_url`. Datas futuras acima de cinco minutos são recusadas para eventos; datas de reserva podem ser futuras.

Histórico de outra plataforma não é importado automaticamente. Antes de qualquer corte é necessário mapear contatos, reservas, opt-out e controle humano. Não reenvie eventos de simulação para “ativar” contatos.

## Agenda e CRM locais

A implementação ativa de calendário é `LocalCalendar`. `POST /v1/slots` cria vagas, sem integração externa. A publicação bloqueia sobreposição por closer. A reserva bloqueia a vaga e impede duas reservas ativas para o mesmo lead; remarcação e cancelamento são transacionais. O CRM local grava notas/tarefas em `bia_go.crm_notes`, com idempotência.

Não há envio de e-mail, criação de Meet, leitura do calendário pessoal do closer ou operação de sessões coletivas. No calendário local a capacidade é uma pessoa por vaga. Eventos de presença devem vir de uma fonte confiável ou de um operador autenticado.

## Gateway futuro de mensagens

O executável só habilita mensagens externas quando configurado com `BIA_GATEWAY_URL`. Essa URL **não é uma API já existente**: o contrato abaixo precisa ser implementado/homologado para o provedor escolhido.

`POST /messages`, `Authorization: Bearer <BIA_GATEWAY_TOKEN>` e `Idempotency-Key: <id da ação>`:

```json
{
  "lead_id": "lead_...",
  "contact": {"name":"Pessoa Exemplo","phone":"+5565999999999","channel":"gateway"},
  "text": "Mensagem da Bia",
  "purpose": "welcome",
  "requires_template": true
}
```

Resposta: `{"id":"id_confirmado_pelo_provedor","status":"accepted"}` ou `sent`. O gateway deve persistir a idempotência antes de repetir qualquer tentativa. Uma resposta 2xx sem recibo não significa envio confirmado. `requires_template` sinaliza que o serviço não dispõe de uma mensagem recebida nas últimas 23 horas; o gateway precisa resolver e validar o template aprovado, consentimento e regras do canal. Primeiro contato por formulário não abre automaticamente uma conversa de WhatsApp.

O adaptador de mensagens não deve aceitar um texto arbitrário como template só porque esse campo é verdadeiro. Mapeie `purpose` para templates aprovados e parâmetros validados. Sem template compatível, recuse com erro explícito e encaminhe para revisão.

Os adaptadores de referência de DataCrazy e ClickMax reproduzem partes dos envelopes encontrados no export. **Não estão ligados no executável**, não foram exercitados ao vivo e não resolvem primeiro contato fora de uma sessão aberta. O envio ClickMax usa `turno_id` estável; DataCrazy trata recibo desconhecido como operação incerta. O adaptador Cal é parcial, inclusive cancelamento deliberadamente indisponível sem contrato verificado. Esses adaptadores não são uma declaração de paridade com a produção.

## Falhas e reprocessamento

Inbox/outbox e estado ficam em transação no Postgres. Workers usam lease de dois minutos por lead, decisão/operação com prazo menor e token que impede um worker atrasado de gravar resultados. Não há transação de banco aberta esperando uma IA responder.

- Falha comprovadamente repetível: até cinco tentativas, com atraso crescente.
- Timeout de uma mutação sem idempotência comprovada: `uncertain`, com revisão humana.
- Processo caiu enquanto executava um efeito: `uncertain`; o lead é pausado para revisão.
- Falha definitiva: `dead`, com motivo e tarefa local quando aplicável.
- Opt-out, tomada humana, reserva substituída, mensagem mais recente ou prazo vencido: o efeito pendente é invalidado antes do envio.

Operações incertas não têm botão genérico de “tentar novamente”. Confira o provedor pelo identificador da operação antes de decidir qualquer reenvio. Nesta etapa, inspeção e reconciliação operacional são feitas por API/SQL local; não foi construído um console de operadores.

Limites iniciais: 15 envios por lead/24h, 60 globais/h, follow-up de oferta após duas horas e no máximo um por 24h, janela de contato proativo das 8h às 20h no fuso do lead. O primeiro contato solicitado pelo formulário é imediato. Lembretes previstos: 24h, 2h e 15min; lembretes vencidos não são acumulados para disparo tardio.

## Dados

O banco mantém perfil, histórico limitado a 30 turnos no estado e payloads dos jobs. Não há política automática de retenção nesta versão; defina-a antes de produção. Logs da aplicação não imprimem tokens, telefones ou corpos de provedores. A chave da OpenAI fica somente no ambiente. `.env` e `.local` são ignorados pelo Git e pelo build Docker.
