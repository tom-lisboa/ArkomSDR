# Conexão OAuth do Google Agenda

Callback implementado no backend Go:

```text
https://sdk.arkom.ai/v1/management/integrations/google/callback
```

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
BIA_GOOGLE_CLIENT_ID=
BIA_GOOGLE_CLIENT_SECRET=
BIA_GOOGLE_REDIRECT_URL=https://sdk.arkom.ai/v1/management/integrations/google/callback
BIA_GOOGLE_TOKEN_KEY=
```

`BIA_GOOGLE_TOKEN_KEY` deve ser uma chave aleatória de pelo menos 32 caracteres. A publicação prepara uma chave de 256 bits no servidor, sem mostrá-la. Guarde-a com o ambiente e os backups: trocar a chave impede abrir as conexões existentes e exige reconexão. O servidor recusa ativar a integração sem os campos obrigatórios.

Nesta entrega, a integração fica desativada até o responsável preencher as credenciais. Para prepará-las localmente, use `micro /Users/tomlisboa/Desktop/Arkon/.env` e preencha apenas `BIA_GOOGLE_CLIENT_ID` e `BIA_GOOGLE_CLIENT_SECRET`; a ativação e sincronização do ambiente do servidor devem ocorrer na publicação, sem copiar segredos para comandos ou mensagens.

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
- O escopo atual é apenas `calendar.calendarlist.readonly`. **Sincronização de reuniões e disponibilidade não está implementada.** Os workers continuam usando a agenda própria no PostgreSQL.

## Validação

Os testes usam PostgreSQL isolado e um transporte Google simulado, sem autorização real ou chamadas à conta de um usuário. Cobrem PKCE, vínculo de navegador, replay e concorrência, recusa, expiração, sessão revogada, alteração de papel, concessões incompletas, isolamento, criptografia, renovação e remoção local. A publicação verifica que a rota existe e que os endpoints protegidos exigem sessão. O teste real de consentimento depende das credenciais e da autorização de uma conta Google.

Referências: [OAuth web do Google](https://developers.google.com/identity/protocols/oauth2/web-server), [biblioteca OAuth2 para Go](https://pkg.go.dev/golang.org/x/oauth2) e [CalendarList](https://developers.google.com/workspace/calendar/api/v3/reference/calendarList/list).
