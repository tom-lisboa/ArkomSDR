<script>
  import { onMount, tick } from 'svelte';
  import { marked } from 'marked';
  import DOMPurify from 'dompurify';
  import Icon from './lib/Icon.svelte';
  import SchemaPanel from './lib/SchemaPanel.svelte';
  import data from './lib/content.generated.json';
  import { operations, curlExample, responseExample } from './lib/api.js';

  const { articles, groups, contract, apiBase } = data;
  const allOperations = operations(contract);
  const schemas = contract.components.schemas;
  const origin = new URL(apiBase).origin;
  const defaults = { inicio: { title: 'Visão geral', icon: 'home' }, api: { title: 'Referência da API', icon: 'code' } };
  const nav = Object.fromEntries([...Object.entries(defaults), ...articles.map(a => [a.id, a])]);
  let route = $state('inicio');
  let selectedID = $state('');
  let menuOpen = $state(false);
  let searchQuery = $state('');
  let operationQuery = $state('');
  let methodFilter = $state('ALL');
  let responseStatus = $state('200');
  let toast = $state('');
  let searchDialog;
  let searchInput;
  let main;
  let toastTimer;

  const normalize = (text) => String(text).normalize('NFD').replace(/[\u0300-\u036f]/g, '').toLowerCase();
  const slug = (text) => normalize(text).replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
  let activeArticle = $derived(articles.find(a => a.id === route));
  let pageTitle = $derived(nav[route]?.title || 'Página não encontrada');
  let filteredOperations = $derived(allOperations.filter(o => (methodFilter === 'ALL' || o.method === methodFilter) && normalize(`${o.method} ${o.path} ${o.summary}`).includes(normalize(operationQuery))));
  let operation = $derived(filteredOperations.find(o => o.id === selectedID) || filteredOperations.find(o => o.shortPath === '/leads' && o.method === 'GET') || filteredOperations[0]);
  let statuses = $derived(operation ? Object.keys(operation.responses) : []);
  let activeStatus = $derived(statuses.includes(responseStatus) ? responseStatus : statuses[0]);
  let responseSchema = $derived(operation?.responses[activeStatus]?.content?.['application/json']?.schema);
  let requestSchema = $derived(operation?.requestBody?.content?.['application/json']?.schema);
  let renderedArticle = $derived(activeArticle ? renderMarkdown(activeArticle.markdown) : '');
  let headings = $derived(activeArticle ? marked.lexer(activeArticle.markdown).filter(t => t.type === 'heading' && t.depth <= 3).map(t => ({ text: t.text.replace(/`/g, ''), id: slug(t.text), depth: t.depth })) : []);
  let articleIndex = $derived(articles.findIndex(a => a.id === route));
  let searchResults = $derived.by(() => {
    const q = normalize(searchQuery.trim());
    if (!q) return [{ title: 'Primeiros passos', description: 'Comece sua integração', href: '#/primeiros-passos', icon: 'rocket', kind: 'Guia' }, { title: 'Referência da API', description: 'Todos os endpoints e schemas', href: '#/api', icon: 'code', kind: 'Referência' }, { title: 'Controle humano', description: 'Assumir, pausar e responder', href: '#/controle-humano', icon: 'hand', kind: 'Guia' }];
    return [
      ...articles.filter(a => normalize(`${a.title} ${a.description} ${a.markdown}`).includes(q)).map(a => ({ title: a.title, description: a.description, href: `#/${a.id}`, icon: a.icon, kind: 'Guia', rank: normalize(a.title).includes(q) ? 0 : 2 })),
      ...allOperations.filter(o => normalize(`${o.path} ${o.summary} ${o.method}`).includes(q)).map(o => ({ title: `${o.method} ${o.shortPath}`, description: o.summary, href: `#/api/${o.id}`, icon: 'code', kind: 'Endpoint', rank: normalize(o.path).includes(q) ? 1 : 3 })),
    ].sort((a,b) => a.rank - b.rank).slice(0, 12);
  });

  function renderMarkdown(markdown) {
    const clean = DOMPurify.sanitize(marked.parse(markdown, { gfm: true }));
    const doc = new DOMParser().parseFromString(clean, 'text/html');
    for (const h of doc.querySelectorAll('h2,h3')) h.id = slug(h.textContent);
    for (const pre of doc.querySelectorAll('pre')) {
      const code = pre.querySelector('code');
      const frame = doc.createElement('div'); frame.className = 'code-frame';
      const bar = doc.createElement('div'); bar.className = 'code-topline';
      const label = doc.createElement('span'); label.textContent = code?.className.replace('language-', '') || 'exemplo';
      const button = doc.createElement('button'); button.type = 'button'; button.textContent = 'Copiar'; button.dataset.copy = encodeURIComponent(code?.textContent || ''); button.setAttribute('aria-label', 'Copiar exemplo de código');
      bar.append(label, button); pre.replaceWith(frame); frame.append(bar, pre);
    }
    for (const table of doc.querySelectorAll('table')) { const wrap = doc.createElement('div'); wrap.className = 'table-scroll'; table.replaceWith(wrap); wrap.append(table); }
    return doc.body.innerHTML;
  }
  function copyBlocks(node) {
    const handler = (event) => { const button = event.target.closest('button[data-copy]'); if (button) copy(decodeURIComponent(button.dataset.copy)); };
    node.addEventListener('click', handler); return { destroy: () => node.removeEventListener('click', handler) };
  }
  async function copy(text, message = 'Copiado para a área de transferência') {
    try { await navigator.clipboard.writeText(text); toast = message; } catch { toast = 'Não foi possível copiar. Selecione o texto e copie manualmente.'; }
    clearTimeout(toastTimer); toastTimer = setTimeout(() => toast = '', 3000);
  }
  async function syncRoute() {
    const parts = location.hash.replace(/^#\/?/, '').split('/');
    route = parts[0] || 'inicio'; selectedID = parts[1] || ''; menuOpen = false;
    operationQuery = ''; methodFilter = 'ALL'; responseStatus = '200';
    if (searchDialog?.open) searchDialog.close();
    await tick(); window.scrollTo({ top: 0, behavior: 'instant' });
    main?.querySelector('h1')?.focus({ preventScroll: true });
  }
  async function openSearch() { searchQuery = ''; searchDialog.showModal(); await tick(); searchInput.focus(); }
  function handleKey(event) {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); searchDialog.open ? searchDialog.close() : openSearch(); }
    if (event.key === 'Escape') menuOpen = false;
  }
  function searchKey(event) {
    if (!['ArrowDown', 'ArrowUp'].includes(event.key)) return;
    const results = [...searchDialog.querySelectorAll('[data-search-result]')];
    if (!results.length) return;
    event.preventDefault(); const i = results.indexOf(document.activeElement); results[(i + (event.key === 'ArrowDown' ? 1 : results.length - 1) + results.length) % results.length].focus();
  }
  function scrollSection(id) { document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' }); }
  function exampleForStatus() {
    if (Number(activeStatus) < 300) return responseExample(operation);
    const errors = { 400:'invalid_json', 401:'unauthorized', 403:'forbidden', 404:'not_found', 409:'contact_busy', 422:'invalid_pagination', 429:'login_rate_limited', 503:'storage_unavailable' };
    return { error: errors[activeStatus] || 'error', request_id: 'identificador_da_solicitacao', retryable: ['409','503'].includes(activeStatus) };
  }
  onMount(() => { syncRoute(); return () => clearTimeout(toastTimer); });
</script>

<svelte:head><title>{pageTitle} · Arkom Docs</title></svelte:head>
<svelte:window onhashchange={syncRoute} onkeydown={handleKey} />
<a class="skip-link" href="#main-content" onclick={(event) => { event.preventDefault(); main.focus(); main.scrollIntoView({block:"start"}); }}>Ir para o conteúdo</a>
<header class="topbar">
  <div class="brand-area">
    <button class="icon-button nav-toggle" aria-label={menuOpen ? 'Fechar navegação' : 'Abrir navegação'} aria-expanded={menuOpen} aria-controls="sidebar" onclick={() => menuOpen = !menuOpen}><Icon name={menuOpen ? 'close' : 'menu'} size={21}/></button>
    <a class="wordmark" href="#/" aria-label="Arkom documentação, início">ARK<span>O</span>M<span class="brand-divider"></span><span class="brand-docs">docs</span></a>
  </div>
  <div class="header-context"><span>Plataforma SDR</span><Icon name="chevron" size={12}/><strong>Desenvolvedores</strong></div>
  <div class="header-actions"><span class="version-tag">v{contract.info.version}</span><a class="repo-link" href="https://github.com/tom-lisboa/ArkomSDR" target="_blank" rel="noreferrer">Repositório <Icon name="external" size={13}/></a><a class="top-download" href="/docs/openapi.json" download="arkom-management-openapi.json"><Icon name="download" size={15}/><span>OpenAPI</span></a></div>
</header>

{#if menuOpen}<button class="nav-scrim" aria-label="Fechar navegação" onclick={() => menuOpen = false}></button>{/if}
<aside id="sidebar" class:mobile-open={menuOpen}>
  <button class="search-trigger" onclick={openSearch}><Icon name="search" size={16}/><span>Buscar na documentação</span><kbd>⌘ K</kbd></button>
  <nav aria-label="Documentação">
    {#each groups as group}<div class="nav-group"><p class="nav-heading">{group.title}</p>{#each group.items as id}<a href={id === 'inicio' ? '#/' : `#/${id}`} class:active={route === id} aria-current={route === id ? 'page' : undefined}><Icon name={nav[id].icon} size={17}/><span>{nav[id].title}</span>{#if route === id}<span class="nav-indicator"></span>{/if}</a>{/each}</div>{/each}
  </nav>
  <div class="sidebar-bottom"><span class="sidebar-badge"><Icon name="code" size={15}/></span><div><strong>Construído em Go</strong><span>Um contrato para toda a equipe.</span></div><span class="small-dot"></span></div>
</aside>

<main id="main-content" tabindex="-1" bind:this={main} class:reference-page={route === 'api'}>
  {#if route === 'inicio'}
    <div class="home-page">
      <div class="eyebrow"><span class="gold-dot"></span> MANAGEMENT API <span class="eyebrow-separator">/</span> GUIA DE INTEGRAÇÃO</div>
      <section class="hero">
        <div class="hero-copy"><h1 tabindex="-1">Seu ponto de partida<br/>para construir o <em>painel.</em></h1><p>Do primeiro contato à reunião. Tudo o que a equipe precisa para conectar uma interface à operação do SDR Arkom.</p><div class="hero-actions"><a class="primary-button" href="#/primeiros-passos">Começar integração <Icon name="arrow" size={17}/></a><a class="text-button" href="#/api">Explorar a API <Icon name="chevron" size={15}/></a></div></div>
        <div class="hero-illustration" aria-hidden="true"><div class="diagram-label">UM CONTEXTO COMPARTILHADO</div><div class="diagram-node front-node"><Icon name="layers" size={18}/><span>Seu frontend</span></div><div class="diagram-wire"></div><div class="diagram-node api-node"><span class="diagram-brand">A</span><div><strong>Arkom API</strong><small>Gestão do SDR</small></div><span class="node-dot"></span></div><div class="diagram-branches"><span>Conversas</span><span>Agenda</span><span>Indicadores</span></div><div class="diagram-footer"><span class="diagram-mini-dot"></span>Go <span>+</span> Postgres</div></div>
      </section>
      <div class="base-url-panel"><div><span class="base-label">BASE DA API</span><code>{apiBase}</code></div><button class="copy-button" aria-label="Copiar URL base da API" onclick={() => copy(apiBase)}><Icon name="copy" size={16}/><span>Copiar</span></button></div>
      <div class="contract-stats"><div><strong>{allOperations.length}</strong><span>operações documentadas</span></div><div><strong>{Object.keys(contract.paths).length}</strong><span>rotas disponíveis</span></div><div><strong>{Object.keys(schemas).length}</strong><span>schemas tipados</span></div><div class="stat-contract"><Icon name="shield" size={20}/><div><strong>OpenAPI 3</strong><span>Contrato direto do backend</span></div></div></div>
      <section class="start-section"><div class="section-heading"><div><span class="eyebrow quiet">POR ONDE COMEÇAR</span><h2>Da conexão ao primeiro atendimento.</h2></div><span class="section-caption">O essencial para a sua integração.</span></div><div class="guide-cards">{#each ['autenticacao','leads','controle-humano'] as id, i}<a class="guide-card" href={`#/${id}`}><div class="card-top"><span class="card-icon"><Icon name={nav[id].icon} size={21}/></span><span class="step-number">0{i+1}</span></div><h3>{nav[id].title}</h3><p>{nav[id].description}</p><span class="card-link">Ler o guia <Icon name="arrow" size={15}/></span></a>{/each}</div></section>
      <section class="integration-section"><div class="integration-copy"><span class="eyebrow quiet">UM FLUXO, VÁRIAS POSSIBILIDADES</span><h2>A operação inteira.<br/>No mesmo contrato.</h2><p>Construa a fila, acompanhe a conversa e dê à equipe o controle do próximo passo.</p><ul><li><Icon name="check" size={16}/> Mensagens, mídias e transcrições.</li><li><Icon name="check" size={16}/> Presença e temperatura do lead.</li><li><Icon name="check" size={16}/> Ações humanas com auditoria.</li></ul><a class="text-button" href="#/arquitetura">Entender a arquitetura <Icon name="arrow" size={16}/></a></div><div class="terminal-card"><div class="terminal-top"><div class="terminal-dots"><i></i><i></i><i></i></div><span>Seu primeiro recurso</span><span class="method get">GET</span></div><div class="terminal-path">/v1/management/leads</div><pre><code><span class="code-muted">// Exemplo de resposta · lista vazia</span>{'\n'}{'{'}{'\n'}  <span class="code-key">"items"</span>: [],{'\n'}  <span class="code-key">"next_cursor"</span>: <span class="code-string">""</span>{'\n'}{'}'}</code></pre><a href={`#/api/${allOperations.find(o => o.shortPath === '/leads' && o.method === 'GET').id}`}>Ver parâmetros e schema <Icon name="arrow" size={15}/></a></div></section>
      <div class="resource-band"><span class="resource-icon"><Icon name="download" size={22}/></span><div><h3>Leve o contrato para o seu ambiente.</h3><p>Importe no Postman ou gere o cliente da aplicação.</p></div><a class="secondary-button" href="/docs/openapi.json" download="arkom-management-openapi.json">Baixar OpenAPI <Icon name="download" size={15}/></a></div>
      <footer class="page-footer"><span>ARKOM <span class="footer-slash">/</span> Documentação para desenvolvedores</span><span>API v{contract.info.version}</span></footer>
    </div>
  {:else if route === 'api'}
    <div class="api-page"><div class="eyebrow quiet">REFERÊNCIA</div><h1 tabindex="-1">Referência da API</h1><p class="page-description">Endpoints, parâmetros e estruturas de dados. Escolha uma operação para consultar o contrato.</p><div class="api-note"><Icon name="info" size={17}/><span>Os exemplos são copiáveis. Nenhuma chamada é executada por este portal.</span><a href="/docs/openapi.json" download="arkom-management-openapi.json">Baixar contrato <Icon name="download" size={14}/></a></div>
    <div class="api-workspace"><div class="operation-browser"><label class="operation-search"><Icon name="search" size={15}/><input aria-label="Filtrar endpoints" placeholder="Filtrar endpoints…" bind:value={operationQuery}/></label><div class="method-filters" aria-label="Filtrar por método">{#each ['ALL','GET','POST','PATCH'] as method}<button class:chosen={methodFilter === method} onclick={() => methodFilter = method}>{method === 'ALL' ? 'Todos' : method}</button>{/each}</div><nav aria-label="Endpoints" class="operation-list">{#each filteredOperations as op}<a class:chosen={operation?.id === op.id} href={`#/api/${op.id}`}><span class={`method ${op.method.toLowerCase()}`}>{op.method}</span><code>{op.shortPath}</code></a>{:else}<div class="no-endpoints"><Icon name="search" size={22}/><p>Nenhum endpoint encontrado.</p><button class="text-button" onclick={() => { operationQuery=''; methodFilter='ALL'; }}>Limpar filtros</button></div>{/each}</nav><div class="operation-count">{filteredOperations.length} de {allOperations.length} operações</div></div>
      {#if operation}<article class="operation-detail"><div class="endpoint-top"><span class={`method ${operation.method.toLowerCase()}`}>{operation.method}</span><code>{operation.shortPath}</code><button class="icon-button" aria-label="Copiar link deste endpoint" onclick={() => copy(`${location.origin}${location.pathname}#/api/${operation.id}`, 'Link do endpoint copiado')}><Icon name="external" size={15}/></button></div><h2>{operation.summary}</h2><div class="operation-meta"><span><Icon name={operation.security?.length === 0 ? 'info' : 'lock'} size={13}/>{operation.security?.length === 0 ? 'Login público' : 'Sessão autenticada'}</span><span>{operation.description}</span></div>
      <section class="endpoint-section"><div class="subheading"><h3>Exemplo de requisição</h3><span>cURL</span></div><div class="code-frame"><div class="code-topline"><span>shell</span><button onclick={() => copy(curlExample(operation, origin))}><Icon name="copy" size={13}/> Copiar</button></div><pre><code>{curlExample(operation, origin)}</code></pre></div></section>
      {#if operation.parameters?.length}<section class="endpoint-section"><h3>Parâmetros</h3><div class="parameter-list">{#each operation.parameters as parameter}<div class="parameter-row"><div><code>{parameter.name}</code><span class="type-label">{parameter.in} · {parameter.schema.type}</span>{#if parameter.required}<span class="required">obrigatório</span>{/if}</div>{#if parameter.description}<p>{parameter.description}</p>{/if}{#if parameter.schema.enum}<div class="enum-values">{#each parameter.schema.enum as value}<code>{value}</code>{/each}</div>{/if}{#if parameter.schema.default !== undefined}<p class="field-constraint">Padrão: {parameter.schema.default}{parameter.schema.maximum ? ` · Máximo: ${parameter.schema.maximum}` : ''}</p>{/if}</div>{/each}</div></section>{/if}
      {#if requestSchema}<section class="endpoint-section"><div class="subheading"><h3>Corpo da requisição</h3><span>application/json</span></div><SchemaPanel schema={requestSchema} {contract}/></section>{/if}
      <section class="endpoint-section"><h3>Respostas</h3><div class="response-codes">{#each statuses as status}<button class:chosen={activeStatus === status} class:success={Number(status) < 300} onclick={() => responseStatus = status}>{status}</button>{/each}</div><p class="response-description">{operation.responses[activeStatus]?.description}</p>{#if exampleForStatus()}<div class="code-frame"><div class="code-topline"><span>Exemplo ilustrativo{operation.shortPath === '/metrics' && Number(activeStatus)<300 ? ' · recorte da resposta' : ''}</span><button onclick={() => copy(JSON.stringify(exampleForStatus(),null,2))}><Icon name="copy" size={13}/> Copiar</button></div><pre><code>{JSON.stringify(exampleForStatus(),null,2)}</code></pre></div>{/if}{#if responseSchema}<SchemaPanel schema={responseSchema} {contract}/>{:else}<div class="binary-note"><Icon name="download" size={18}/> Conteúdo binário com tipo de mídia validado.</div>{/if}</section>
      </article>{/if}
    </div></div>
  {:else if activeArticle}
    <div class="article-layout"><article class="doc-article"><div class="eyebrow quiet">{activeArticle.group}</div><h1 tabindex="-1">{activeArticle.title}</h1><p class="page-description">{activeArticle.description}</p><div class="article-divider"></div><div class="prose" use:copyBlocks>{@html renderedArticle}</div><div class="doc-pagination">{#if articleIndex > 0}<a href={`#/${articles[articleIndex-1].id}`}><span>← Anterior</span><strong>{articles[articleIndex-1].title}</strong></a>{:else}<a href="#/"><span>← Anterior</span><strong>Visão geral</strong></a>{/if}{#if articleIndex < articles.length-1}<a href={`#/${articles[articleIndex+1].id}`} class="next"><span>Próximo →</span><strong>{articles[articleIndex+1].title}</strong></a>{:else}<a href="#/api" class="next"><span>Consultar →</span><strong>Referência da API</strong></a>{/if}</div><footer class="page-footer"><span>Arkom · Guia de integração</span><button onclick={() => copy(location.href,'Link do guia copiado')}><Icon name="copy" size={13}/> Copiar link</button></footer></article><aside class="article-toc"><p>NESTA PÁGINA</p>{#each headings as heading}<button class:sub={heading.depth === 3} onclick={() => scrollSection(heading.id)}>{heading.text}</button>{/each}<div class="toc-help"><Icon name="code" size={17}/><strong>Procurando um campo?</strong><p>Explore o contrato e os schemas de cada operação.</p><a href="#/api">Abrir referência <Icon name="arrow" size={13}/></a></div></aside></div>
  {:else}
    <div class="not-found"><span class="eyebrow quiet">404 · DOCUMENTAÇÃO</span><h1 tabindex="-1">Este guia não foi encontrado.</h1><p>Use a busca ou volte à visão geral para continuar.</p><a class="primary-button" href="#/">Voltar ao início <Icon name="arrow" size={16}/></a></div>
  {/if}
</main>

<dialog bind:this={searchDialog} class="search-dialog" onkeydown={searchKey} aria-label="Buscar na documentação"><div class="search-dialog-input"><Icon name="search" size={20}/><input bind:this={searchInput} bind:value={searchQuery} placeholder="Buscar guias, endpoints, métricas…" aria-label="Termo de busca"/><button class="icon-button" aria-label="Fechar busca" onclick={() => searchDialog.close()}><kbd>ESC</kbd></button></div><div class="search-results"><p class="search-label">{searchQuery ? `${searchResults.length} resultados` : 'ACESSO RÁPIDO'}</p>{#each searchResults as result}<a href={result.href} data-search-result onclick={() => searchDialog.close()}><span class="search-result-icon"><Icon name={result.icon}/></span><div><strong>{result.title}</strong><span>{result.description}</span></div><small>{result.kind}</small><Icon name="arrow" size={15}/></a>{:else}<div class="search-empty"><Icon name="search" size={28}/><h3>Nenhum resultado encontrado</h3><p>Tente buscar por “presença”, “leads” ou “login”.</p></div>{/each}</div><div class="search-dialog-footer"><span><kbd>↑</kbd><kbd>↓</kbd> navegar</span><span><kbd>↵</kbd> abrir</span><span><kbd>ESC</kbd> fechar</span></div></dialog>
<div class="toast" class:visible={!!toast} role="status" aria-live="polite">{#if toast}<Icon name="check" size={16}/>{toast}{/if}</div>
