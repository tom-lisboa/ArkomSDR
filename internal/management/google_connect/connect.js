const API = '/v1/management';
const byID = (id) => document.getElementById(id);
let token = '';
let loading = false;
let generation = 0;
let nextPage = '';
let calendarCount = 0;
let userRole = '';

const errors = {
  invalid_credentials: 'Usuário ou senha incorretos. Confira seu acesso e tente novamente.',
  login_rate_limited: 'Houve muitas tentativas de acesso. Aguarde alguns minutos antes de tentar novamente.',
  google_not_configured: 'A conexão com o Google ainda não foi liberada. Avise o administrador do SDR.',
  google_not_connected: 'Sua conta Google ainda não está conectada.',
  google_refresh_failed: 'O acesso ao Google precisa ser renovado. Use Reconectar Google Agenda.',
  google_calendar_unavailable: 'Não foi possível consultar suas agendas agora. Você pode atualizar ou reconectar a conta.',
  forbidden: 'Seu acesso ainda não permite conectar agendas. Peça ao administrador um usuário de vendedor.',
  unauthorized: 'Seu acesso expirou. Entre novamente para continuar.',
};

function message(text = '', success = false) {
  byID('message').textContent = text;
  byID('message').hidden = !text;
  byID('message').classList.toggle('success', success);
}

async function api(path, method = 'GET', data) {
  const headers = { Accept: 'application/json' };
  if (token) headers.Authorization = `Bearer ${token}`;
  if (data !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch(API + path, { method, headers, cache: 'no-store', credentials: 'omit', body: data === undefined ? undefined : JSON.stringify(data), signal: AbortSignal.timeout(25000) });
  const result = await response.json().catch(() => ({}));
  if (!response.ok) {
    if (response.status === 401 && token) reset();
    const error = new Error(errors[result.error] || 'Não foi possível concluir agora. Tente novamente em instantes.');
    error.code = result.error;
    throw error;
  }
  return result;
}

function reset() {
  token = '';
  userRole = '';
  generation++;
  nextPage = '';
  byID('loginView').hidden = false;
  byID('accountView').hidden = true;
  byID('stepLogin').classList.add('active');
  byID('stepGoogle').classList.remove('active');
  byID('continueLink').hidden = true;
  byID('continueLink').removeAttribute('href');
  byID('returnHelp').hidden = true;
  byID('disconnectConfirm').hidden = true;
  byID('calendarList').replaceChildren();
  byID('createSeller').hidden = true;
  byID('createSeller').open = false;
  byID('createSellerForm').reset();
  byID('sellerPassword').type = 'password';
  byID('showSellerPassword').textContent = 'Mostrar senha';
  byID('sellerMessage').hidden = true;
  message();
}

async function calendars(append = false) {
  const current = generation;
  if (!append) {
    byID('calendarList').replaceChildren();
    calendarCount = 0;
    nextPage = '';
  }
  const result = await api('/integrations/google/calendars' + (append && nextPage ? '?page_token=' + encodeURIComponent(nextPage) : ''));
  if (current !== generation || !token) return;
  for (const calendar of result.items || []) {
    const item = document.createElement('li');
    const title = document.createElement('strong');
    const detail = document.createElement('span');
    title.textContent = calendar.summary || calendar.id;
    detail.textContent = calendar.id + (calendar.primary ? ' · Agenda principal' : '');
    item.append(title, detail);
    byID('calendarList').append(item);
    calendarCount++;
  }
  nextPage = result.next_page_token || '';
  byID('calendarCount').textContent = `${calendarCount} ${calendarCount === 1 ? 'agenda' : 'agendas'}`;
  byID('calendars').hidden = false;
  byID('moreButton').hidden = !nextPage;
}

async function refresh() {
  if (!token || loading) return;
  const current = generation;
  loading = true;
  byID('refreshButton').disabled = true;
  try {
    const status = await api('/integrations/google/status');
    if (current !== generation || !token) return;
    byID('disconnectButton').hidden = !status.connected;
    byID('connectButton').disabled = !status.configured;
    byID('connectLabel').textContent = status.connected ? 'Reconectar Google Agenda' : 'Conectar Google Agenda';
    byID('connectionTitle').textContent = status.connected ? 'Sua conta Google está conectada' : 'Conecte sua conta Google';
    byID('statusText').textContent = status.connected ? 'Conexão salva no seu usuário' : status.configured ? 'Aguardando sua autorização no Google' : 'A conexão ainda não foi liberada';
    byID('statusDot').classList.toggle('connected', status.connected);
    byID('calendars').hidden = true;
    if (status.connected) {
      await calendars();
      if (current !== generation || !token) return;
      byID('statusText').textContent = 'Conectado · acesso às agendas confirmado';
      byID('continueLink').hidden = true;
      byID('returnHelp').hidden = true;
      message('Pronto. Sua conta Google foi conectada ao seu acesso no SDR.', true);
    }
  } catch (error) {
    if (current === generation || error.code === 'unauthorized') {
      message(error.message || 'Não foi possível verificar a conexão. Tente novamente.');
      byID('statusDot').classList.remove('connected');
    }
  } finally {
    loading = false;
    byID('refreshButton').disabled = false;
  }
}

byID('loginForm').addEventListener('submit', async (event) => {
  event.preventDefault();
  byID('loginButton').disabled = true;
  message();
  try {
    const result = await api('/auth/login', 'POST', { username: byID('username').value.trim(), password: byID('password').value });
    token = result.access_token;
    generation++;
    byID('password').value = '';
    if (!['operator', 'admin'].includes(result.user?.role)) {
      await api('/auth/logout', 'POST').catch(() => {});
      reset();
      message(errors.forbidden);
      return;
    }
    const name = result.user.name || result.user.username;
    userRole = result.user.role;
    byID('displayName').textContent = name;
    byID('displayUsername').textContent = result.user.username;
    byID('avatar').textContent = name.slice(0, 1).toUpperCase();
    byID('loginView').hidden = true;
    byID('accountView').hidden = false;
    byID('createSeller').hidden = userRole !== 'admin';
    byID('stepGoogle').classList.add('active');
    await refresh();
  } catch (error) {
    message(error.message || 'Não foi possível entrar. Confira sua conexão e tente novamente.');
  } finally {
    byID('loginButton').disabled = false;
  }
});

byID('connectButton').addEventListener('click', async () => {
  if (!token) return;
  // Open during the user's click so browser popup protection does not block the
  // Google flow. The new tab cannot access this page or its in-memory session.
  const tab = window.open('about:blank', '_blank');
  if (tab) {
    tab.opener = null;
    tab.document.title = 'Abrindo Google Agenda';
    tab.document.body.textContent = 'Preparando a conexão com o Google…';
  }
  const current = generation;
  byID('connectButton').disabled = true;
  message();
  try {
    const flow = await api('/integrations/google/authorize', 'POST');
    if (current !== generation || !token) { tab?.close(); return; }
    const url = new URL(flow.authorization_url);
    if (url.origin !== window.location.origin || url.pathname !== '/v1/management/integrations/google/start') throw new Error('Não foi possível abrir a autorização. Tente novamente.');
    byID('continueLink').href = url.href;
    byID('continueLink').hidden = !!tab;
    byID('returnHelp').hidden = false;
    if (tab) tab.location.replace(url.href);
  } catch (error) {
    tab?.close();
    message(error.message || 'Não foi possível iniciar a conexão com o Google.');
  } finally {
    byID('connectButton').disabled = false;
  }
});

byID('refreshButton').addEventListener('click', () => { message(); refresh(); });
window.addEventListener('focus', () => { if (!byID('disconnectConfirm').hidden) return; refresh(); });
byID('moreButton').addEventListener('click', async () => {
  byID('moreButton').disabled = true;
  try { await calendars(true); } catch (error) { message(error.message); }
  finally { byID('moreButton').disabled = false; }
});
byID('disconnectButton').addEventListener('click', () => { byID('disconnectConfirm').hidden = false; });
byID('cancelDisconnect').addEventListener('click', () => { byID('disconnectConfirm').hidden = true; });
byID('confirmDisconnect').addEventListener('click', async () => {
  byID('confirmDisconnect').disabled = true;
  try {
    await api('/integrations/google/disconnect', 'POST');
    generation++;
    byID('disconnectConfirm').hidden = true;
    byID('continueLink').hidden = true;
    byID('continueLink').removeAttribute('href');
    byID('returnHelp').hidden = true;
    await refresh();
    message('Sua conta Google foi desconectada deste acesso.');
  } catch (error) { message(error.message); }
  finally { byID('confirmDisconnect').disabled = false; }
});
byID('logoutButton').addEventListener('click', async () => {
  byID('logoutButton').disabled = true;
  try {
    if (token) await api('/auth/logout', 'POST');
    reset();
  } catch (error) { message(error.message); }
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

// Enable submission only after all handlers are ready; native fallback uses POST.
byID('loginButton').disabled = false;
