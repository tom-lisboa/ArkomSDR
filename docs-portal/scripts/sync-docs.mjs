import { readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const portal = path.join(root, 'docs-portal');
const source = await readFile(path.join(root, 'docs/management-backend.md'), 'utf8');
const contract = JSON.parse(await readFile(path.join(root, 'internal/management/openapi.json'), 'utf8'));
const apiBase = new URL('/v1/management', contract.servers[0].url).href;
function section(title) {
  const marker = `## ${title}\n`;
  const i = source.indexOf(marker);
  if (i < 0) throw new Error(`Missing source section: ${title}`);
  return source.slice(i + marker.length).split('\n## ')[0].trim();
}
const code = (lang, text) => '\n\n```' + lang + '\n' + text + '\n```\n\n';
const articles = [
  { id:'primeiros-passos', title:'Primeiros passos', group:'Comece por aqui', icon:'rocket', description:'Da primeira sessão à leitura dos leads, em três passos.', markdown:
`Integre o painel à API de gestão do SDR. A mesma base reúne cadastros, conversas, reservas, controle humano e indicadores.

## 1. Crie uma sessão
Peça à equipe um usuário com o papel adequado. Os valores abaixo são exemplos: substitua pelo seu acesso.` + code('sh',`curl --request POST '${apiBase}/auth/login' \\\n  --header 'Content-Type: application/json' \\\n  --data '{"username":"seu.usuario","password":"SUA_SENHA"}'`) + `A resposta contém \`access_token\`, \`expires_at\` e \`user\`. A sessão vale por 12 horas.

## 2. Consulte os leads
Use o token recebido no cabeçalho \`Authorization\`.` + code('sh',`curl '${apiBase}/leads?limit=20' \\\n  --header 'Authorization: Bearer SEU_ACCESS_TOKEN'`) + `A lista retorna \`items\` e \`next_cursor\`. Cada lead inclui perfil, etapa, controle da automação, reserva, responsável, score e quantidade de follow-ups.

## 3. Construa a ficha
Use o \`id\` retornado para consultar mensagens, ligações, follow-ups e reservas. Uma pessoa pode estar agendada e sob atendimento humano ao mesmo tempo.

| Recurso | Caminho |
|---|---|
| Ficha | \`GET /leads/{id}\` |
| Histórico | \`GET /leads/{id}/messages\` |
| Ligações | \`GET /leads/{id}/calls\` |
| Reservas | \`GET /leads/{id}/bookings\` |
| Indicadores | \`GET /metrics\` |

## Próximo passo
Consulte a referência para conhecer os parâmetros e schemas. Baixe o OpenAPI para importar no Postman ou gerar o cliente da aplicação. Os exemplos deste portal são documentação e não executam chamadas.` },
  { id:'autenticacao', title:'Autenticação e permissões', group:'Comece por aqui', icon:'lock', description:'Sessões, papéis e acesso do futuro frontend.', markdown:
`A API de gestão usa usuários próprios. Solicite um acesso individual à equipe responsável pela operação.

## Login e sessão` + code('http',`POST /v1/management/auth/login\nContent-Type: application/json\n\n{\n  "username": "seu.usuario",\n  "password": "SUA_SENHA"\n}`) + `Envie \`Authorization: Bearer <access_token>\` nas chamadas seguintes. A validade é de 12 horas, informada em \`expires_at\`.

## Papéis da equipe
| Papel | Permissões |
|---|---|
| \`viewer\` | Consultar leads, histórico, transcrições e indicadores. |
| \`operator\` | Consultar e executar ações de atendimento. |
| \`admin\` | Atender e administrar usuários e permissões. |

## Encerramento e revogação
\`POST /auth/logout\` revoga a sessão atual. Desativação do usuário, troca de senha ou mudança de papel também revogam sessões. Ao receber \`401 unauthorized\`, encaminhe a pessoa novamente ao login.

## Integração no navegador
Solicite o cadastro da origem exata do frontend para habilitar CORS. O backend aceita uma lista explícita de origens; não usa curinga. Um BFF pode chamar a API no servidor.

O \`BIA_API_TOKEN\` é uma credencial interna do SDR e não autentica neste painel. O frontend deve usar o fluxo de sessão dos usuários.

## Limites de login
São permitidas dez tentativas por nome em quinze minutos, com teto compartilhado de 120 por endereço da conexão. Um \`429\` inclui \`Retry-After\`. Respeite o período informado antes de repetir.` },
  { id:'leads', title:'Leads e etapas', group:'Operação', icon:'layers', description:'Modele a fila sem misturar jornada e automação.', markdown:
`Uma pessoa tem uma etapa na jornada, uma situação comercial e um estado de automação. Esses campos devem aparecer separadamente no painel.

## Fila e filtros` + code('http','GET /v1/management/leads?status=open&temperature=hot&limit=20') + `Use \`q\` para pesquisar nome, empresa, telefone ou e-mail. Combine \`stage\`, \`status\`, \`automation\`, \`temperature\` e \`assigned_to\` quando necessário.

## Etapas da jornada
| Código | Significado |
|---|---|
| \`contact_pending\` | Contato inicial pendente |
| \`contacted\` | Primeiro contato realizado |
| \`engaged\` | Cliente respondeu |
| \`scheduling\` | Escolha de horário ou espera por disponibilidade |
| \`booked\` | Reunião agendada |
| \`presence_confirmed\` | Intenção de presença confirmada |
| \`in_meeting\` | Dentro do intervalo previsto da reunião |
| \`awaiting_attendance\` | Reunião terminou e aguarda resultado |
| \`attended\` | Comparecimento registrado |
| \`no_show\` | Falta registrada |
| \`cancelled\` | Reserva cancelada |

## Controle e situação comercial
\`automation\` pode ser \`active\`, \`human\`, \`paused\`, \`opted_out\` ou \`no_consent\`. A situação \`status\` pode ser \`open\`, \`closed\`, \`won\` ou \`lost\`.

Um lead agendado pode estar em atendimento humano. Reunião confirmada não significa venda; confirmação de intenção de presença não comprova comparecimento.

## Ficha do lead
\`GET /leads/{id}\` reúne formulário, reserva, horários oferecidos, memória comercial, resultados anteriores e flags operacionais. Use \`revision\` para proteger ações que dependem da ficha atualizada.` },
  { id:'controle-humano',title:'Controle humano',group:'Operação',icon:'hand',description:'Assuma, responda e devolva a conversa à IA.',markdown:section('Comandos da equipe').replace('| `kind`','## Ações disponíveis\n\n| `kind`').replace('Assumir ou pausar invalida','## Interromper e retomar\n\nAssumir ou pausar invalida').replace('Tomar controle revoga','## Encerramento de ligação\n\nTomar controle revoga').replace('Resposta manual exige','## Respostas e encerramento\n\nResposta manual exige')},
  { id:'comparecimento',title:'Reuniões e comparecimento',group:'Operação',icon:'calendar',description:'Registre presença e mantenha a IA informada.',markdown:'## Registrar o resultado'+code('json','{\n  "kind": "attendance",\n  "booking_id": "booking_exemplo",\n  "outcome": "attended",\n  "reason": "Presença conferida pela equipe"\n}')+'Envie para `POST /leads/{id}/actions`, com `Idempotency-Key`.\n\n## Regras de comparecimento\n\n'+section('Comparecimento e memória da IA')+'\n\n## Resultado pendente\n\nQuando a reunião terminar sem resultado registrado, mantenha o comparecimento pendente. A passagem do horário não transforma silêncio em falta.'},
  { id:'alertas-vendedores', title:'Avisos aos vendedores', group:'Operação', icon:'users', description:'QR individual e alertas de leads parados no funil.', markdown:section('Avisos aos vendedores') },
  { id:'temperatura',title:'Temperatura do lead',group:'Dados e indicadores',icon:'thermometer',description:'Os sinais que formam o score de engajamento.',markdown:section('Temperatura matemática').replace('| Evidência observável','## Sinais e pesos\n\n| Evidência observável').replace('A soma é limitada','## Faixas e interpretação\n\nA soma é limitada')},
  { id:'metricas',title:'Métricas e conversão',group:'Dados e indicadores',icon:'chart',description:'Definições, denominadores e períodos para os indicadores.',markdown:section('Métricas e definições').replace('- `snapshot`:','## Grupos de indicadores\n\n- `snapshot`:').replace('As taxas são frações','## Unidades e leitura\n\nAs taxas são frações')},
  { id:'paginacao',title:'Paginação e histórico',group:'Dados e indicadores',icon:'list',description:'Carregue filas, mensagens e transcrições.',markdown:
`Listagens usam \`limit\` de 1 a 100, com padrão 50. O cursor da própria resposta indica como buscar a próxima página.

## Cursores por recurso
| Recurso | Parâmetro | Campo da resposta | Ordem |
|---|---|---|---|
| Leads | \`cursor\` | \`next_cursor\` | Atualização, decrescente |
| Mensagens, ligações e reservas | \`cursor\` | \`next_cursor\` | Data, decrescente |
| Follow-ups e auditoria | \`before_seq\` | \`next_before_seq\` | Sequência, decrescente |
| Transcrição | \`after_seq\` | \`next_after_seq\` | Sequência, crescente |

Um \`next_cursor\` vazio ou cursor de sequência \`null\` indica fim da lista. Trate o cursor como opaco.

## Mensagens e mídias
O histórico informa canal, remetente, conteúdo, finalidade e entrega. Diferencie mensagens recebidas, pendentes, enviadas, puladas e incertas. Aceitação do provedor não comprova leitura.

Mídias recebidas fornecem \`download_path\` para consulta autenticada. Busque o arquivo com a sessão e gere uma URL de objeto para exibir no navegador, quando necessário.

## Transcrições e atualização
As transcrições identificam \`user\` e \`assistant\`, com sequência e data. Falas da assistente são persistidas após reconhecimento de reprodução. Não há gravação de áudio de ligação.

IDs de ligação podem conter barras; codifique o ID como um segmento da URL. O painel pode atualizar a ficha por consultas periódicas. Esta versão não oferece SSE ou WebSocket para o painel.` },
  { id:'erros',title:'Erros e idempotência',group:'Referência',icon:'shield',description:'Quando repetir, atualizar a ficha ou corrigir a intenção.',markdown:
`Leia \`error\` e \`retryable\` para decidir o próximo passo. O cabeçalho \`X-Request-ID\` ajuda a equipe a investigar a solicitação.

## Resposta de erro` + code('json','{\n  "error": "contact_busy",\n  "retryable": true,\n  "request_id": "identificador_da_solicitacao"\n}') + `## Casos frequentes
| HTTP / código | Próximo passo |
|---|---|
| 401 · \`unauthorized\` | Refazer o login quando a sessão estiver ausente, expirada ou revogada. |
| 401 · \`invalid_credentials\` | Conferir usuário e senha. |
| 403 · \`forbidden\` | Usar a permissão exigida. |
| 403 · \`origin_not_allowed\` | Solicitar cadastro da origem do frontend. |
| 409 · \`contact_busy\` | Aguardar o efeito externo e repetir a mesma intenção e chave. |
| 409 · \`revision_conflict\` | Atualizar a ficha e confirmar a intenção. |
| 409 · \`idempotency_conflict\` | Conferir a chave, já usada para outro corpo ou lead. |
| 409 · \`whatsapp_window_closed\` | A janela para resposta manual não está aberta. |
| 409 · \`human_reply_not_permitted\` | Conferir controle humano, pausa, autorização e opt-out. |
| 409 · \`meeting_not_finished\` | Respeitar início para presença e fim para registrar falta. |
| 422 | Corrigir campos ou paginação. |
| 429 · \`login_rate_limited\` | Aguardar \`Retry-After\`. |
| 503 · \`storage_unavailable\` | Tratar a indisponibilidade conforme \`retryable\`. |

## Uma chave por intenção
Ações exigem \`Idempotency-Key\`. Repetir a mesma intenção com a mesma chave e usuário retorna a operação anterior. Não reutilize a chave com outro corpo ou lead.` + code('http','POST /v1/management/leads/lead_exemplo/actions\nAuthorization: Bearer SEU_ACCESS_TOKEN\nContent-Type: application/json\nIdempotency-Key: 5ae438c0-209e-4f98-8b94-c3f353004124\n\n{ "kind": "takeover", "reason": "Vou continuar o atendimento" }') + 'Uma resposta manual aceita fica em `delivery_status=queued`. Consulte o histórico antes de apresentar o envio como concluído.'},
  { id:'arquitetura',title:'Arquitetura e contexto',group:'Referência',icon:'network',description:'Como a gestão se conecta aos dados e workers do SDR.',markdown:
`A API consulta o mesmo Postgres do SDR e usa seus fluxos de execução. O painel não precisa manter outra cópia dos cadastros ou reconstruir o contexto.

## Caminho de uma ação
1. O frontend envia a ação com sessão e chave de idempotência.
2. O Go verifica permissão, ficha e regras operacionais.
3. A auditoria registra ator, intenção, resultado e revisão.
4. O worker realiza o envio pela fila existente.
5. O histórico informa o resultado confirmado pelo transporte.

## Serviços do SDR
| Serviço | Responsabilidade |
|---|---|
| API | Contratos HTTP, autenticação, formulários e painel. |
| Contexto | Conversas, fragmentos e continuidade da memória. |
| Execução | Seleção de especialista e respostas. |
| Mídia | Interpretação de áudio/imagem e envio de mídia. |
| Agenda | Disponibilidade, reserva, troca e cancelamento. |
| Follow-up | Cadências, lembretes e recuperação autorizada. |
| Voz | Permissão, ligação, transcrição e encerramento. |

## Contexto compartilhado
O comparecimento registrado atualiza a reunião e a memória dos agentes de texto e voz. Notas internas permanecem na auditoria e não são enviadas ao cliente nem inseridas automaticamente no prompt.

## Escopo deste portal
Aqui você consulta documentação e exemplos. Dados reais de pessoas e ações de atendimento continuam protegidos pela API autenticada.`},
];
articles.splice(articles.findIndex(a=>a.id==='paginacao'),0,{
 id:'cadencias',title:'Follow-ups e botões',group:'Operação',icon:'calendar',description:'Retomadas sem resposta e avisos para comparecer à reunião.',markdown:`O acompanhamento usa o worker de follow-up e o mesmo histórico do SDR. Cada etapa tem identidade e prazo próprios.

## Sem resposta à abertura
| Momento | Próximo passo |
|---|---|
| 15 minutos | Retomar a abertura e facilitar a escolha. |
| 2 horas | Explicar o valor da conversa e oferecer horários. |
| 6 horas | Esclarecer como funciona antes de escolher um horário. |
| Dia seguinte | Conferir se ainda faz sentido seguir. |
| 2 dias | Convidar a retomar o cadastro e tirar uma dúvida. |
| 3 dias | Último contato da sequência. |

As seis etapas oferecem um próximo passo por botão. Em 6h, **Ver horários**, **Como funciona** e **Agora não**. Em 48h, **Quero conversar**, **Tenho uma dúvida** e **Agora não**. Nas demais, **Ver horários**, **Tirar dúvida** e **Agora não**. Param com resposta, reserva, pausa, saída ou atendimento humano. Respeitam os horários de contato e não se acumulam após reinício. A ativação tem um corte explícito: contatos anteriores não entram automaticamente.

## Reunião marcada
| Momento | Canal e ação |
|---|---|
| 15 minutos após abertura | WhatsApp de preparação, quando ainda houver antecedência. |
| 24 horas antes | WhatsApp para manter ou remarcar o horário. |
| 2 horas antes | WhatsApp para confirmar o compromisso. |
| 1 hora antes | WhatsApp para manter o horário, remarcar ou pedir ajuda. |
| 30 minutos antes | Áudio já preparado. |
| 15 minutos antes | WhatsApp com botões e pedido nativo de permissão da ligação. |
| 5 minutos antes | WhatsApp e SMS para quem autorizou SMS. |
| No início | WhatsApp para confirmar, remarcar ou pedir ajuda. |
| 7 minutos após início | Ajuda somente sem resposta ou confirmação recente. |

Os avisos oferecem **Confirmar presença**, **Remarcar** e **Preciso de ajuda**. A chamada continua dependendo da permissão nativa. O aviso posterior ao início não afirma que a pessoa faltou; o comparecimento continua sendo registrado pela equipe.

## Entrega e histórico
Consulte \`GET /leads/{id}/followups\`. Os propósitos \`no_reply_*\` identificam retomadas e \`meeting_checkin_*\` os novos avisos de reunião. Eles entram nos contadores existentes. Mostre situação e motivo de descarte: pendente, enviado, pulado e falho não significam a mesma coisa.

Fora da sessão recente de WhatsApp, o transporte exige o modelo específico aprovado. Enquanto um modelo estiver em análise, o job aguarda sem ultrapassar sua validade. Dentro da sessão, os botões de sessão podem ser usados. Essa restrição não deve aparecer como se uma mensagem já tivesse sido entregue.`
});
const groups = [
 {title:'Comece por aqui',items:['inicio','primeiros-passos','autenticacao']},
 {title:'Operação',items:['leads','controle-humano','comparecimento','cadencias','alertas-vendedores']},
 {title:'Dados e indicadores',items:['temperatura','metricas','paginacao']},
 {title:'Referência',items:['api','erros','arquitetura']},
];
const data = { apiBase, contract, articles, groups };
await writeFile(path.join(portal,'src/lib/content.generated.json'),JSON.stringify(data));
await writeFile(path.join(portal,'public/openapi.json'),JSON.stringify(contract,null,2)+'\n');
await writeFile(path.join(portal,'public/guia-integracao.md'),'# Arkom — Guia de integração\n\nBase: '+apiBase+'\n\n'+articles.map(a=>'# '+a.title+'\n\n'+a.markdown).join('\n\n---\n\n')+'\n');
console.log(`Synced ${articles.length} guides and ${Object.keys(contract.paths).length} API paths.`);
