# Conexão OAuth do Google Agenda

Callback implementado no backend Go:

```text
https://sdk.arkom.ai/v1/management/integrations/google/callback
```

## Acesso dos vendedores

O endereço fixo para compartilhar com a equipe é **https://sdk.arkom.ai/conectar-agenda/**. A página é servida pelo backend Go e pode ser usada sem aguardar um botão no painel externo.

Cada vendedor entra com seu próprio usuário do SDR (`operator` ou `admin`) e clica em **Adicionar conta Google**. A autorização abre em outra aba. Depois de permitir o acesso no Google, ele volta à página para conferir a conexão e as agendas disponíveis. Cada vendedor pode adicionar várias contas; cada conta pode conter várias agendas. A página identifica cada conta por e-mail e permite listar suas agendas, renovar sua autorização e desconectar somente aquela conta, com confirmação.

O endereço é comum à equipe; a autorização gerada é individual e vinculada à sessão de quem entrou. A sessão do SDR permanece apenas na memória da página, sem armazenamento em `localStorage` ou `sessionStorage`. Recarregar a página exige entrar novamente. Nenhuma conta é conectada ou criada automaticamente.

O administrador pode entrar na mesma página e usar **Criar acesso de vendedor**, informando nome, usuário e senha inicial. A criação usa a API administrativa existente e atribui `operator`, que permite atendimento de leads. A senha é enviada somente na criação; o administrador deve compartilhar o acesso individual com o vendedor. Nenhum cadastro ocorre automaticamente. O usuário compartilhado de visualização não pode criar acessos, autorizar conexões ou atender leads. Os nomes e e-mails dos vendedores não foram informados nesta entrega, portanto não foram criados usuários novos nem alteradas permissões de contas existentes.

## Preparar o Google Console

1. Habilite Google Calendar API no projeto desejado.
2. Configure a tela de consentimento e, se o aplicativo estiver em teste, adicione os usuários de teste.
3. Crie um cliente OAuth do tipo **Aplicativo Web** e cadastre a URI de redirecionamento acima, sem barra adicional.
4. Salve Client ID e Client Secret no ambiente privado. Eles não devem ir para o frontend ou para o Git. O domínio raiz e `/docs/` não são callbacks.

O fluxo atual é no servidor. Não usa o SDK JavaScript do Google no navegador. Uma origem JavaScript cadastrada não substitui a URI de redirecionamento.

## Ambiente

```dotenv
BIA_MANAGEMENT_ENABLED=true
BIA_GOOGLE_ENABLED=true
BIA_GOOGLE_SCHEDULING_ENABLED=true
BIA_GOOGLE_CLIENT_ID=
BIA_GOOGLE_CLIENT_SECRET=
BIA_GOOGLE_REDIRECT_URL=https://sdk.arkom.ai/v1/management/integrations/google/callback
BIA_GOOGLE_TOKEN_KEY=
```

`BIA_GOOGLE_TOKEN_KEY` deve ser uma chave aleatória de pelo menos 32 caracteres. A publicação prepara uma chave de 256 bits no servidor, sem mostrá-la. Guarde-a com o ambiente e os backups: trocar a chave impede abrir as conexões existentes e exige reconexão. O servidor recusa ativar a integração sem os campos obrigatórios.

A integração é desativada por padrão. Para preparar as credenciais localmente, use `micro /Users/tomlisboa/Desktop/Arkon/.env` e preencha apenas `BIA_GOOGLE_CLIENT_ID` e `BIA_GOOGLE_CLIENT_SECRET`; a ativação e sincronização do ambiente do servidor devem ocorrer na publicação, sem copiar segredos para comandos ou mensagens. Neste servidor, as credenciais do aplicativo já foram configuradas; a autorização de cada conta Google continua sendo feita pelo próprio vendedor.

Para atualizar somente essas duas credenciais e ativar o OAuth no release já publicado:

```sh
python3 deploy/publish-google-oauth.py --configure-only
```

O comando exige os dois valores no `.env` local, preserva a chave de criptografia existente e recarrega somente a API. Não use esse modo com um release anterior à implementação do OAuth. Não apaga cadastros ou reservas, nem altera os endereços da Twilio.

## Contrato para o frontend

Veja [Google Agenda no guia de gestão](management-backend.md#google-agenda) e o OpenAPI em `/docs/openapi.json`. Todos os caminhos usam o prefixo `/v1/management`:

| Método e caminho | Acesso |
| --- | --- |
| `POST /integrations/google/authorize` | Bearer de operator/admin; retorna URL temporária |
| `GET /integrations/google/start?ticket=...` | Navegação de uso único criada pelo endpoint anterior |
| `GET /integrations/google/callback` | Retorno do Google, protegido por estado, cookie e PKCE |
| `GET /integrations/google/status` | Bearer; conexão do próprio usuário |
| `GET /integrations/google/calendars` | Bearer de operator/admin; consulta e renovação de acesso |
| `GET /integrations/google/scheduling` | Bearer de operator/admin, perfil do próprio vendedor |
| `PUT /integrations/google/scheduling` | Bearer de operator/admin, agenda de destino e agendas de bloqueio |
| `POST /integrations/google/disconnect` | Bearer de operator/admin; remoção local |

O frontend deve navegar até a URL retornada por `authorize`, no mesmo navegador que concluirá o consentimento. Isso estabelece o cookie `__Host-bia-google-oauth` no domínio do callback sem exigir cookies de terceiros em uma chamada cross-origin. Não coloque o token de sessão do painel em uma URL. O callback devolve HTML simples para navegação e JSON para os demais clientes.

## Garantias e limites

- Tentativas são vinculadas ao usuário e à sessão, expiram em dez minutos e só podem ser iniciadas uma vez.
- Estado e cookie são aleatórios; somente seus hashes são armazenados. O código de autorização não é persistido.
- A tentativa é consumida em uma operação já confirmada antes de consultar o Google. Falha ou timeout da troca não autoriza replay.
- Verificador PKCE e tokens são cifrados com AES-GCM, com associação ao cliente OAuth, finalidade e usuário/tentativa. Não podem ser transplantados entre usuários.
- Tokens são cifrados no PostgreSQL, nunca devolvidos ao frontend ou colocados em logs de aplicação.
- Requisições usam HTTPS, prazos limitados e não seguem redirecionamentos dos endpoints de tokens/dados.
- Tentativas substituídas, logout, revogação de sessão e remoção local impedem concluir um retorno pendente. Consultas e operações da mesma conexão são serializadas durante as alterações de credenciais.
- Refresh token rotacionado é salvo antes da consulta de agendas, inclusive se essa consulta posterior falhar.
- O endpoint de desconexão remove a concessão do SDR. A revogação no Google é uma ação separada na própria conta.
- A identidade é obtida no UserInfo HTTPS do Google e exige e-mail verificado. `calendar.events.freebusy` autoriza consultar ocupação e `calendar.events` autoriza criar e cancelar eventos.

## Validação

Os testes usam PostgreSQL isolado e um transporte Google simulado, sem autorização real ou chamadas à conta de um usuário. Cobrem PKCE, vínculo de navegador, replay e concorrência, recusa, expiração, sessão revogada, alteração de papel, concessões incompletas, isolamento, criptografia, renovação e remoção local. A publicação verifica que a rota existe e que os endpoints protegidos exigem sessão. O teste real de consentimento depende das credenciais e da autorização de uma conta Google.

Referências: [OAuth web do Google](https://developers.google.com/identity/protocols/oauth2/web-server), [biblioteca OAuth2 para Go](https://pkg.go.dev/golang.org/x/oauth2) e [CalendarList](https://developers.google.com/workspace/calendar/api/v3/reference/calendarList/list).

## Várias contas e compatibilidade

A migração 21 guarda contas em `google_accounts`, com ID próprio e unicidade por vendedor, cliente OAuth e subject Google. Conexões antigas são copiadas uma vez com a cifra anterior e podem continuar listando calendários. Ao abrir o token, a cifra é atualizada para o contexto da conta. As credenciais antigas associadas à conexão migrada também são removidas quando ela é desconectada. O registro de migração impede que ela seja recriada em uma execução posterior.

Nova autorização sem `account_id` adiciona uma identidade; a mesma identidade é atualizada sem duplicação. Para reconexão, forneça o ID alvo; outra identidade Google é rejeitada. `status` lista todas as contas e `account_count`. Para calendários e desconexão, use `account_id`; chamadas antigas sem ID só escolhem automaticamente quando há uma única conta. Com mais de uma, retornam `google_account_id_required`.

A identidade (subject) não é enviada ao navegador. E-mail e ID da conexão são metadados da conta do próprio vendedor. Cada token novo é associado criptograficamente ao usuário e à conta, impedindo cópia de credenciais entre duas contas do mesmo usuário. Dados de identidade nunca são aceitos como entrada do frontend.

## Ativar agendamentos do SDR interno

1. Entre com seu usuário individual e conecte as contas Google que quer usar. Se a conta já estava conectada, clique em **Reconectar** para conceder as permissões de agenda.
2. Em **Reuniões pelo SDR**, clique em **Escolher agendas**. Escolha a agenda que deve receber os eventos e marque as agendas que devem bloquear horários. A agenda de destino sempre bloqueia horários.
3. Clique em **Ativar agendamento na minha agenda**. Para parar de receber novas reuniões, use **Pausar novos agendamentos**. Reservas já confirmadas continuam registradas.

O worker consulta as agendas marcadas, considera a grade de 45 minutos e escolhe, entre os vendedores livres, o que tem menos reservas futuras no SDR. O lead recebe confirmação depois de a reserva existir no PostgreSQL e na agenda de destino. Remarcações conservam o vendedor quando a reserva já está integrada. O cancelamento só libera a vaga local depois de confirmar a remoção no Google. Os eventos são criados sem convidados e sem link automático de sala.

A migração 22 guarda a configuração e as operações em andamento. Uma resposta perdida do Google é recuperada pelo identificador estável do evento. Enquanto a confirmação estiver pendente, o horário fica retido. Se nenhum vendedor estiver habilitado, a disponibilidade retorna vazia. Reservas antigas permanecem consultáveis. Uma conta com reuniões futuras ou operações pendentes precisa ser mantida conectada para concluir a sincronização.

Consulte [ocupação no Google Agenda](https://developers.google.com/workspace/calendar/api/v3/reference/freebusy/query) e [criação de eventos](https://developers.google.com/workspace/calendar/api/v3/reference/events/insert).
