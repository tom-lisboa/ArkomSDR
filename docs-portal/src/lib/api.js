export function operations(contract) {
  return Object.entries(contract.paths).flatMap(([path, methods]) => Object.entries(methods).filter(([method]) => ['get','post','patch','put','delete'].includes(method)).map(([method, value]) => ({ ...value, method: method.toUpperCase(), path, shortPath: path.replace('/v1/management', ''), id: value.operationId })));
}
export function requestExample(operation) {
  const name = operation.requestBody?.content?.['application/json']?.schema?.$ref?.split('/').at(-1);
  return {
    Login: { username: 'seu.usuario', password: 'SUA_SENHA' },
    UserCreate: { username: 'novo.operador', name: 'Operador Exemplo', role: 'operator', password: 'SENHA_INDIVIDUAL_FORTE' },
    UserUpdate: { enabled: true, role: 'operator' },
    Action: { kind: 'takeover', reason: 'Vou continuar o atendimento', expected_revision: 12 },
  }[name] || null;
}
export function curlExample(operation, origin) {
  const path = operation.path.replace('{id}', 'lead_exemplo').replace('{user}', 'usr_exemplo').replace('{call}', 'call_exemplo').replace('{asset}', 'media_exemplo');
  const lines = [`curl --request ${operation.method} '${origin}${path}'`];
  if (!operation.security || operation.security.length) lines.push("  --header 'Authorization: Bearer SEU_ACCESS_TOKEN'");
  if (operation.parameters?.some(p => p.name === 'Idempotency-Key')) lines.push("  --header 'Idempotency-Key: 5ae438c0-209e-4f98-8b94-c3f353004124'");
  const body = requestExample(operation);
  if (body) { lines.push("  --header 'Content-Type: application/json'"); lines.push(`  --data '${JSON.stringify(body)}'`); }
  return lines.join(' \\\n');
}
export function responseExample(operation) {
  const user = { id: 'usr_exemplo', username: 'seu.usuario', name: 'Operador Exemplo', role: 'operator', enabled: true };
  if (operation.shortPath === '/auth/login') return { access_token: 'TOKEN_DA_SESSAO', token_type: 'Bearer', expires_at: '2026-10-30T06:00:00Z', user: { id: 'usr_exemplo', username: 'seu.usuario', name: 'Operador Exemplo', role: 'operator', enabled: true } };
  if (operation.shortPath.endsWith('/actions') && operation.method === 'POST') return { result: { action_id: 'op_exemplo', lead_id: 'lead_exemplo', kind: 'takeover', revision: 13, human: true, paused: false, at: '2026-10-29T18:00:00Z' }, duplicate: false };
  if (operation.shortPath.endsWith('/transcript')) return { items: [], next_after_seq: null, recording_available: false };
  if (operation.shortPath.endsWith('/followups') || operation.shortPath.endsWith('/actions')) return { items: [], next_before_seq: null };
  if (operation.shortPath === '/users' && operation.method === 'GET') return { items: [] };
  if (operation.shortPath === '/users' || operation.shortPath === '/users/{user}' || operation.shortPath === '/auth/me') return user;
  if (operation.shortPath === '/auth/logout') return { logged_out: true };
  if (operation.shortPath === '/leads' || operation.shortPath.endsWith('/messages') || operation.shortPath.endsWith('/calls') || operation.shortPath.endsWith('/bookings')) return { items: [], next_cursor: '' };
  if (operation.shortPath === '/metrics') return { rate_unit: 'fraction', score_version: 'engagement-v1', meeting_cohort: { attended: 0, no_show: 0, completed_without_outcome: 0, show_up_rate: null, show_up_sample_size: 0 } };
  return null;
}
