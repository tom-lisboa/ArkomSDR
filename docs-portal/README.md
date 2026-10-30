# Arkom Docs

Portal público da documentação da API de gestão, feito em Svelte 5 e Vite.

**Publicado:** <https://24.199.100.186/docs/>

O portal documenta a API; o painel operacional será construído pela equipe. A leitura é pública e não faz chamadas autenticadas nem contém dados de leads ou credenciais. Exemplos usam valores fictícios. O acesso aos dados reais continua exigindo uma sessão da API.

## Desenvolvimento

Use Node.js 22.12+ ou 24 e npm:

```sh
cd docs-portal
npm ci
npm run dev
```

Abra <http://127.0.0.1:4187/docs/>. O portal usa rotas por hash, incluindo links diretos para cada guia e operação, busca local e exemplos copiáveis. Funciona em desktop e celular, com menu lateral recolhível.

## Conteúdo e contrato

- `../internal/management/openapi.json` é a fonte dos endpoints, parâmetros e schemas.
- `../docs/management-backend.md` contém o guia técnico do backend.
- `scripts/sync-docs.mjs` seleciona somente conteúdo adequado à documentação pública e monta os dez guias. Instruções administrativas de operação e credenciais ficam fora do portal.
- `npm run sync` atualiza os arquivos gerados. O comando também roda antes de desenvolvimento, check e build.

Depois de alterar o contrato ou os guias, gere e publique novamente. O portal é uma cópia estática vinculada à publicação; não lê o OpenAPI autenticado em tempo real.

## Verificar e publicar

```sh
cd docs-portal
npm run check
npm run build
cd ..
python3 deploy/publish-docs.py
```

O check confere sincronização com o OpenAPI do Go, navegação, exemplos, compilação Svelte e ausência de caminhos operacionais nos guias públicos. A publicação envia apenas `dist/`, valida o Caddy e troca o link do release estático. Não publica `.env`, não reinicia containers e não altera registros do SDR. Se a configuração do Caddy tiver mudado fora do repositório, a publicação para para revisão. Se o reload ou a verificação HTTPS falhar, a configuração e o release anteriores são restaurados.

Destino padrão: servidor `dev@24.199.100.186`, identidade `~/.ssh/arkom_dev`, diretório `/var/www/arkom-docs`. Outros acessos podem ser passados com `--host` e `--identity`. A URL pública e o Caddy correspondem a este ambiente; altere os dois ao mudar de servidor. Node.js só é necessário para compilar; produção serve arquivos estáticos pelo Caddy.
