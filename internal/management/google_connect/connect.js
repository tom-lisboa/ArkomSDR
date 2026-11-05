const API = '/v1/management';
const byID = (id) => document.getElementById(id);
let token = '', userRole = '';
let generation = 0, refreshing = false;
let statusRequest = 0;
let accountSignature = '', pendingSignature = null;
const errors = {
 invalid_credentials: 'Usuário ou senha incorretos. Confira seu acesso e tente novamente.',
 login_rate_limited: 'Houve muitas tentativas de acesso. Aguarde alguns minutos antes de tentar novamente.',
 google_not_configured: 'A conexão com o Google ainda não foi liberada. Avise o administrador do SDR.',
 google_not_connected: 'Você ainda não adicionou uma conta Google.',
 google_account_not_found: 'Esta conta não está mais conectada ao seu acesso. Atualize as conexões.',
 google_account_id_required: 'Escolha a conta Google para esta ação.',
 google_account_already_connected: 'Esta conta já está conectada em outro cartão. Use a conta já identificada.',
 google_account_mismatch: 'Foi escolhida outra conta Google. Para reconectar, selecione o mesmo e-mail mostrado no cartão.',
 google_identity_unavailable: 'Não foi possível confirmar a conta Google. Reinicie a autorização e permita identificar seu e-mail.',
 google_refresh_failed: 'O acesso a esta conta precisa ser renovado. Use Reconectar nesta conta.',
 google_calendar_unavailable: 'Não foi possível consultar as agendas desta conta. Tente novamente ou reconecte a conta.',
 forbidden: 'Seu acesso ainda não permite conectar agendas. Peça ao administrador um usuário de vendedor.',
 unauthorized: 'Seu acesso expirou. Entre novamente para continuar.',
};
function message(text = '', success = false) {
 byID('message').textContent = text; byID('message').hidden = !text;
 byID('message').classList.toggle('success', success);
}
async function api(path, method = 'GET', data) {
 const headers = { Accept: 'application/json' };
 const requestToken = token;
 if (requestToken) headers.Authorization = `Bearer ${requestToken}`;
 if (data !== undefined) headers['Content-Type'] = 'application/json';
 const response = await fetch(API + path, { method, headers, cache: 'no-store', credentials: 'omit', body: data === undefined ? undefined : JSON.stringify(data), signal: AbortSignal.timeout(25000) });
 const result = await response.json().catch(() => ({}));
 if (!response.ok) {
  if (response.status === 401 && requestToken && token === requestToken) reset();
  const error = new Error(errors[result.error] || 'Não foi possível concluir agora. Tente novamente em instantes.');
  error.code = result.error; throw error;
 }
 return result;
}
function reset() {
 token = ''; userRole = ''; generation++; pendingSignature = null; accountSignature = '';
 statusRequest++; refreshing = false;
 byID('loginView').hidden = false; byID('accountView').hidden = true;
 byID('stepLogin').classList.add('active'); byID('stepGoogle').classList.remove('active');
 byID('continueLink').hidden = true; byID('continueLink').removeAttribute('href'); byID('returnHelp').hidden = true;
 byID('accountCards').replaceChildren(); byID('createSeller').hidden = true; byID('createSeller').open = false;
 byID('createSellerForm').reset(); byID('sellerPassword').type = 'password';
 byID('showSellerPassword').textContent = 'Mostrar senha'; byID('sellerMessage').hidden = true;
 message();
}
function action(label, handler, className = 'text-button') {
 const button = document.createElement('button'); button.type = 'button'; button.className = className;
 button.textContent = label; button.addEventListener('click', handler); return button;
}
function renderAccounts(accounts) {
 byID('accountCards').replaceChildren();
 for (const account of accounts) {
  const card = document.createElement('section'); card.className = 'google-account';
  const title = document.createElement('h3'); title.textContent = account.email || 'Conta Google anterior';
  const description = document.createElement('p'); description.className = 'muted small';
  description.textContent = account.requires_reconnect ? 'Reconecte para confirmar a identidade desta conta.' : 'Conta vinculada ao seu usuário';
  const actions = document.createElement('div'); actions.className = 'account-actions';
  const feedback = document.createElement('p'); feedback.className = 'help'; feedback.hidden = true;
  const list = document.createElement('ul'); list.hidden = true;
  let page = '', count = 0, loading = false;
  const more = action('Ver mais agendas', () => loadCalendars(true)); more.hidden = true;
  async function loadCalendars(append = false) {
   if (loading) return; loading = true; const current = generation;
   view.disabled = true; more.disabled = true; feedback.hidden = false; feedback.textContent = 'Consultando agendas…';
   try {
    const query = new URLSearchParams({ account_id: account.id });
    if (append && page) query.set('page_token', page);
    const result = await api('/integrations/google/calendars?' + query);
    if (current !== generation || !token || !card.isConnected) return;
    if (result.account_id !== account.id) throw new Error('Atualize as conexões antes de consultar esta conta.');
    if (!append) { list.replaceChildren(); count = 0; }
    for (const calendar of result.items || []) {
     const item = document.createElement('li'), name = document.createElement('strong'), detail = document.createElement('span');
     name.textContent = calendar.summary || calendar.id;
     detail.textContent = calendar.id + (calendar.primary ? ' · Agenda principal' : '');
     item.append(name, detail); list.append(item); count++;
    }
    page = result.next_page_token || ''; list.hidden = false; more.hidden = !page;
    feedback.textContent = `${count} ${count === 1 ? 'agenda disponível' : 'agendas disponíveis'} nesta conta.`;
   } catch (error) { if (current === generation) feedback.textContent = error.message; }
   finally { loading = false; view.disabled = false; more.disabled = false; }
  }
  const view = action('Ver agendas', () => loadCalendars());
  const reconnect = action('Reconectar', () => connectAccount(account.id));
  const disconnect = action('Desconectar', () => {
   confirmation.hidden = false; actions.hidden = true;
  }, 'text-button danger');
  actions.append(view, reconnect, disconnect);
  const confirmation = document.createElement('div'); confirmation.className = 'confirmation'; confirmation.hidden = true;
  const question = document.createElement('p'); question.textContent = `Desconectar ${account.email || 'esta conta Google'} do seu usuário?`;
  const controls = document.createElement('div'); controls.className = 'confirm-actions';
  const cancel = action('Manter conexão', () => { confirmation.hidden = true; actions.hidden = false; }, 'secondary');
  const confirm = action('Desconectar conta', async () => {
   confirm.disabled = true; const current = generation;
   try {
    await api('/integrations/google/disconnect', 'POST', { account_id: account.id });
    if (current !== generation || !token) return;
    pendingSignature = null; byID('continueLink').hidden = true; byID('returnHelp').hidden = true;
    generation++; await refresh(true); message('Conta Google desconectada deste acesso.');
   } catch (error) { message(error.message); }
   finally { confirm.disabled = false; }
  }, 'primary danger-bg');
  controls.append(cancel, confirm); confirmation.append(question, controls);
  card.append(title, description, actions, feedback, list, more, confirmation); byID('accountCards').append(card);
 }
}
async function refresh(force = false) {
 if (!token || (refreshing && !force)) return;
 const serial = ++statusRequest;
 refreshing = true; const current = generation; byID('refreshButton').disabled = true;
 try {
  const status = await api('/integrations/google/status');
  if (current !== generation || !token || serial !== statusRequest) return;
  const accounts = Array.isArray(status.accounts) ? status.accounts : [];
  const signature = accounts.map(a => a.id + ':' + a.updated_at).join('|');
  byID('connectButton').disabled = !status.configured;
  byID('statusText').textContent = !status.configured ? 'A conexão ainda não foi liberada' : accounts.length ? `${accounts.length} ${accounts.length === 1 ? 'conta Google vinculada' : 'contas Google vinculadas'}` : 'Nenhuma conta Google adicionada';
  byID('statusDot').classList.toggle('connected', accounts.length > 0);
  renderAccounts(accounts);
  if (pendingSignature !== null && signature !== pendingSignature) {
   byID('continueLink').hidden = true; byID('returnHelp').hidden = true; pendingSignature = null;
   message('Pronto. A autorização da conta Google foi salva.', true);
  }
  accountSignature = signature;
 } catch (error) { if (current === generation || error.code === 'unauthorized') message(error.message || 'Não foi possível verificar as conexões.'); }
 finally { if (serial === statusRequest) { refreshing = false; byID('refreshButton').disabled = false; } }
}
async function connectAccount(accountID = '') {
 if (!token) return;
 const tab = window.open('about:blank', '_blank');
 if (tab) { tab.opener = null; tab.document.title = 'Abrindo Google Agenda'; tab.document.body.textContent = 'Preparando a conexão com o Google…'; }
 const current = generation; byID('connectButton').disabled = true; message();
 try {
  const flow = await api('/integrations/google/authorize', 'POST', accountID ? { account_id: accountID } : {});
  if (current !== generation || !token) { tab?.close(); return; }
  const url = new URL(flow.authorization_url);
  if (url.origin !== window.location.origin || url.pathname !== '/v1/management/integrations/google/start') throw new Error('Não foi possível abrir a autorização. Tente novamente.');
  pendingSignature = accountSignature;
  byID('continueLink').href = url.href; byID('continueLink').hidden = !!tab; byID('returnHelp').hidden = false;
  if (tab) tab.location.replace(url.href);
 } catch (error) { tab?.close(); message(error.message || 'Não foi possível iniciar a conexão com o Google.'); }
 finally { byID('connectButton').disabled = false; }
}
byID('loginForm').addEventListener('submit', async (event) => {
 event.preventDefault(); byID('loginButton').disabled = true; message();
 try {
  const result = await api('/auth/login', 'POST', { username: byID('username').value.trim(), password: byID('password').value });
  token = result.access_token; generation++; byID('password').value = '';
  if (!['operator', 'admin'].includes(result.user?.role)) { await api('/auth/logout', 'POST').catch(() => {}); reset(); message(errors.forbidden); return; }
  userRole = result.user.role;
  const name = result.user.name || result.user.username;
  byID('displayName').textContent = name; byID('displayUsername').textContent = result.user.username;
  byID('avatar').textContent = name.slice(0, 1).toUpperCase(); byID('loginView').hidden = true;
  byID('accountView').hidden = false; byID('createSeller').hidden = userRole !== 'admin'; byID('stepGoogle').classList.add('active');
  await refresh();
 } catch (error) { message(error.message || 'Não foi possível entrar. Confira sua conexão e tente novamente.'); }
 finally { byID('loginButton').disabled = false; }
});
byID('connectButton').addEventListener('click', () => connectAccount());
byID('refreshButton').addEventListener('click', () => { message(); refresh(); });
window.addEventListener('focus', () => refresh(true));
byID('logoutButton').addEventListener('click', async () => {
 byID('logoutButton').disabled = true;
 try { if (token) await api('/auth/logout', 'POST'); reset(); }
 catch (error) { message(error.message); }
 finally { byID('logoutButton').disabled = false; }
});

byID('showSellerPassword').addEventListener('click', () => {
  const field = byID('sellerPassword');
  field.type = field.type === 'password' ? 'text' : 'password';
  byID('showSellerPassword').textContent = field.type === 'password' ? 'Mostrar senha' : 'Ocultar senha';
});
byID('createSellerForm').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (!token || userRole !== 'admin') return;
  byID('createSellerButton').disabled = true;
  byID('sellerMessage').hidden = true;
  const current = generation;
  try {
    const user = await api('/users', 'POST', {
      username: byID('sellerUsername').value.trim().toLowerCase(),
      name: byID('sellerName').value.trim(),
      password: byID('sellerPassword').value,
      role: 'operator',
    });
    if (current !== generation || !token) return;
    byID('createSellerForm').reset();
    byID('sellerPassword').type = 'password';
    byID('showSellerPassword').textContent = 'Mostrar senha';
    byID('sellerMessage').textContent = `Acesso ${user.username} criado. Envie o usuário e a senha inicial ao vendedor, junto com o endereço desta página.`;
    byID('sellerMessage').classList.add('success');
    byID('sellerMessage').hidden = false;
  } catch (error) {
    if (current !== generation || !token) return;
    byID('sellerMessage').textContent = error.code === 'invalid_or_existing_user' ? 'Confira os dados. O usuário deve ter letras minúsculas, números, ponto ou traço, e a senha precisa ter pelo menos 12 caracteres. Esse usuário também pode já existir.' : error.message;
    byID('sellerMessage').classList.remove('success');
    byID('sellerMessage').hidden = false;
  } finally { byID('createSellerButton').disabled = false; }
});


byID('loginButton').disabled = false;
