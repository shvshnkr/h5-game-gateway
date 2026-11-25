/* h5-game-gateway PWA shell — vanilla JS, no build step */
let TOKEN = localStorage.getItem('token') || '';

const $ = id => document.getElementById(id);

async function api(path, body, method) {
  const r = await fetch(path, {
    method: method || (body ? 'POST' : 'GET'),
    headers: Object.assign({'Content-Type': 'application/json'},
      TOKEN ? {Authorization: 'Bearer ' + TOKEN} : {}),
    body: body ? JSON.stringify(body) : undefined,
  });
  const j = await r.json();
  if (r.status === 401 && TOKEN) { logout(); }
  return j;
}

async function register() {
  const j = await api('/api/auth/register', {username: $('username').value, password: $('password').value});
  if (j.token) { afterAuth(j); } else { $('authmsg').textContent = j.error || 'error'; }
}

async function login() {
  const j = await api('/api/auth/login', {username: $('username').value, password: $('password').value});
  if (j.token) { afterAuth(j); } else { $('authmsg').textContent = j.error || 'error'; }
}

function afterAuth(j) {
  TOKEN = j.token;
  localStorage.setItem('token', TOKEN);
  $('auth').hidden = true;
  $('wallet').hidden = false;
  refreshBalance();
  loadGames();
}

function logout() {
  TOKEN = '';
  localStorage.removeItem('token');
  $('auth').hidden = false;
  $('wallet').hidden = true;
  $('balance').textContent = '—';
}

async function refreshBalance() {
  const j = await api('/api/wallet');
  if (j.balance !== undefined) $('balance').textContent = j.balance.toFixed(4) + ' ' + (j.currency || 'USD');
}

async function deposit() {
  const j = await api('/api/wallet/deposit', {amount: parseFloat($('dep_amount').value)});
  if (!j.deposit_id) return;
  // mock confirmation: in production the crypto processor (Bitcart etc.)
  // POSTs the signed webhook from its server. Here we do it from the client
  // for the demo — sha256 (WebCrypto), same scheme as gateway expects.
  const rt = String(Math.floor(Date.now() / 1000));
  const sha = async s => {
    const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(s));
    return [...new Uint8Array(buf)].map(b => b.toString(16).padStart(2, '0')).join('');
  };
  const sign = await sha('DEMO' + rt + 'dev-operator-secret' + 'webhook');
  const w = await api('/api/wallet/webhook', {deposit_id: j.deposit_id, status: 'confirmed',
                                              operator_code: 'DEMO', request_time: rt, sign: sign});
  refreshBalance();
}

async function loadGames() {
  const j = await api('/api/games');
  const box = $('games');
  box.innerHTML = '';
  (j.games || []).forEach(g => {
    const d = document.createElement('div');
    d.className = 'game';
    d.innerHTML = `<img src="${g.thumbnail}" alt=""><span>${g.name}</span>`;
    d.onclick = () => launch(g.game_code, g.name);
    box.appendChild(d);
  });
}

async function launch(code, name) {
  const j = await api('/api/games/launch', {game_code: code});
  if (!j.url) return;
  $('gametitle').textContent = name;
  $('gameframe').src = j.url;
  $('gamemodal').hidden = false;
  // refresh balance when the player closes the game
  $('gameframe').onload = refreshBalance;
}

function closeGame() {
  $('gamemodal').hidden = true;
  $('gameframe').src = 'about:blank';
  refreshBalance();
}

if (TOKEN) { afterAuth({token: TOKEN}); }

if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {});
}
