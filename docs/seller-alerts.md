# Alertas internos de leads parados

Implementação em Go sobre a API Lumi existente em `https://lume-io.com`, consultada no repositório local `Desktop/Data/lumi` e na [documentação da Lumi](https://lume-io.com/docs). A API pública de envio utilizada é `POST /message/sendText`, com a chave da instância em `X-API-Key`; gerenciamento e QR usam a chave de usuário.

O [guia para o frontend](management-backend.md#avisos-aos-vendedores) documenta conexão, consulta de estado, preferências e disparo manual. O contrato está no OpenAPI. Não há frontend operacional de vendedores neste pacote.

## Configuração

```dotenv
BIA_LUMI_URL=https://lume-io.com
BIA_LUMI_USER_API_KEY=
BIA_SALES_STALLED_AFTER=30m
BIA_MANAGEMENT_ENABLED=true
```

A chave é de **usuário**, com acesso à criação e gestão de instâncias. Cada vendedor continua tendo uma instância isolada, pareada com seu aparelho por QR. A chave de instância retornada pela criação é guardada no Postgres com AES-GCM, autenticada pelo ID do vendedor e derivada do token interno do SDR. Não aparece nos retornos da API, no portal ou nos logs. A rotação do token interno exige recifrar essas credenciais antes da troca ou recuperar as conexões pela Lumi; não descarte a chave de cifragem sem planejar essa migração.

O usuário adiou o preenchimento da chave nesta implementação. Sem ela, os endpoints de integração respondem `503 lumi_not_configured`; não são criadas instâncias nem enviados alertas reais. As variáveis foram preparadas no `.env` local. Depois de preencher, execute da raiz do repositório:

```sh
python3 deploy/configure-lumi.py
```

Esse comando valida a chave com uma consulta de instâncias, salva somente as três configurações Lumi no ambiente remoto e recria `api` e `followup`. Não cria instâncias nem dispara mensagens de teste. Vendedores só entram na audiência depois de autenticar no SDR, criar a conexão, parear o QR e manter avisos habilitados.

## Persistência e execução

- Schema 18: `seller_whatsapp`, `sales_progress` e `sales_notifications`, no schema `bia_go` existente.
- O processo `followup` verifica conexões e funil a cada 30 segundos e consome o ledger de alertas a cada dois segundos. Essa rotina independe da cadência de follow-ups ao cliente.
- O funil usa a mesma projeção SQL da API de gestão. O relógio usa eventos de negócio e entradas do cliente; alterações técnicas de `leads.updated_at` e envios automáticos não adiam o alerta.
- A audiência é formada por usuários ativos com papel `operator` ou `admin`, avisos habilitados, instância conectada e número confirmado. O envio é para o próprio número pareado, em mensagens para si mesmo.
- O POST e a varredura periódica usam um bloqueio de transação para atualizar episódios e uma restrição única por lead/episódio/vendedor. O consumidor usa claim persistente e `SKIP LOCKED`; uma queda durante envio vira `uncertain`, sem replay automático.
- Novas entradas e avanços de etapa invalidam avisos pendentes. Encerramento comercial, pausa, opt-out, falta de consentimento, desativação do vendedor e logout também impedem novos envios.
- Modo `shadow` registra `simulated`, sem executar `sendText`. Um `202` do POST do SDR confirma enfileiramento, não recebimento pelo vendedor.

## Recuperação e limites

A Lumi atual não deduplica `sendText` nem criação de instância por chave externa. O SDR evita repetir operações ambíguas. Em `uncertain`, confira o ledger `outbound_messages` da Lumi antes de qualquer reenvio manual. `sent` confirma envio pela Lumi, sem recibo de leitura no WhatsApp.

Se a criação de instância falhar ou for interrompida depois de alcançar a Lumi, a conexão fica em `review_required` ou `provisioning`. Repetir o POST não cria outra instância. Um administrador deve conferir o nome determinístico `Arkom SDR - <user_id>` no painel Lumi e reconciliar a conexão/chave no banco antes de liberar a operação; não gere novas instâncias às cegas. Essa recuperação administrativa não tem endpoint público nesta versão.

O compartilhamento aqui é o alerta interno solicitado: nome, empresa, telefone, etapa, espera e link WhatsApp. Não são enviados histórico completo, transcrições, faturamento ou credenciais.

## Verificações

Os testes usam PostgreSQL isolado e servidor HTTP de Lumi simulado, sem mensagens reais. Cobrem broadcast para dois vendedores, autenticação e papéis, cifragem, redirects sem vazamento de credenciais, concorrência, idempotência compartilhada com o scheduler, reuniões futuras, pausa, interação nova, descarte de aviso obsoleto, desativação de avisos, opt-out e erro ambíguo sem repetição.

O pareamento e recebimento no aparelho precisam ser homologados depois de configurar a chave da Lumi. A estrutura do SDR e a estrutura da Lumi permanecem separadas; não foi necessário alterar o repositório da Lumi.
