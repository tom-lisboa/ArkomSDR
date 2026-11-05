# Domínio público do backend

Publicado em 05/11/2026: **https://sdk.arkom.ai**.

- `/` redireciona para `/docs/`.
- `/docs/` serve a documentação Svelte existente.
- `/healthz` verifica a API Go.
- `/conectar-agenda/` permite que vendedores entrem com o acesso individual e conectem a própria conta Google. HTML, CSS e JavaScript são servidos pelo Go; os dados continuam exigindo a sessão autenticada.
- `/v1/management/*` encaminha para a API de gestão, mantendo a autenticação do backend.
- `/webhooks/test-form/*` encaminha para o formulário, mantendo a chave e as validações existentes.
- Os demais caminhos retornam 404. A API interna não foi exposta.

## TLS e Cloudflare

O DNS está com proxy Cloudflare ativo. O Caddy no servidor `24.199.100.186` possui um certificado público Let's Encrypt para `sdk.arkom.ai`, com renovação automática. O certificado inicial da origem é válido até 03/02/2027. A validação HTTP-01 permanece habilitada; TLS-ALPN-01 foi desabilitada neste domínio porque o proxy termina a conexão TLS externa.

A origem está preparada para **Full (strict)**. A configuração desse modo na conta Cloudflare não foi consultada nem alterada nesta publicação. A conexão pública pela Cloudflare e a conexão direta à origem com SNI `sdk.arkom.ai` foram verificadas com validação normal de certificado. O erro 525 observado antes da instalação desapareceu.

Verificações realizadas:

| Verificação | Resultado |
| --- | --- |
| HTTPS do domínio pela Cloudflare | 200 em `/healthz` e `/docs/` |
| HTTPS direto na origem com o nome do domínio | Certificado e nome válidos; `/healthz` 200 |
| HTTP do domínio | Redireciona para HTTPS |
| Gestão sem sessão | 401 em `/v1/management/auth/me` |
| HTTPS antigo por IP | `/healthz` e `/docs/` continuam 200 |
| API e voz na rede local do servidor | Verificações de saúde 200 |

Não houve troca do release Go nem reinicialização dos workers. Os outros hosts e rotas presentes no Caddy compartilhado foram preservados. As URLs da Twilio e `BIA_VOICE_PUBLIC_URL` continuam no endereço anterior, pois a assinatura dos webhooks depende da URL cadastrada.

## Configuração e recuperação

O bloco deste domínio está em `deploy/Caddyfile.sdk` e é instalado como `/etc/caddy/arkom-sdk.caddy`. O Caddyfile compartilhado contém um import absoluto desse arquivo.

Para publicar uma alteração deste bloco:

```sh
python3 deploy/configure-sdk-domain.py
```

O instalador faz backup da configuração, adiciona somente o import, valida o Caddy e recarrega o serviço. Em caso de erro de validação ou recarga, restaura a configuração anterior. Os backups ficam em `/etc/caddy/backups/sdk-*`. Não substitua o Caddyfile compartilhado inteiro pelo arquivo deste repositório: o servidor hospeda outras aplicações.

As evidências desta instalação ficam em `.local/deploy/sdk-domain/`, fora do Git.

## Google Agenda

O callback OAuth está implementado no Go em **`https://sdk.arkom.ai/v1/management/integrations/google/callback`**. Essa é a URI exata a cadastrar no cliente Web do Google Console. Veja [a configuração e os limites da integração](google-agenda.md).

A ativação depende de Client ID e Client Secret salvos no ambiente do servidor e de uma autorização feita pelo usuário no Google. Enquanto desativada, a rota responde `503 google_not_configured`. A conexão permite listar agendas e renovar tokens; a sincronização de reservas ainda não foi implementada. A agenda do SDR permanece no PostgreSQL.

Referências: [HTTPS automático do Caddy](https://caddyserver.com/docs/automatic-https), [Full (strict) da Cloudflare](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full-strict/) e [OAuth para aplicações web do Google](https://developers.google.com/identity/protocols/oauth2/web-server).
