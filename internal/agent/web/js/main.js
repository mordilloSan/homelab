// The entry point: refresh, the page-wide controls, the router and the timers.
import {$, confirmAction, dur, lastOk, post, setLastOk, setStatus, st, toLogin} from './core.js';
import {renderGlobal, renderHud, renderTopology, renderWires} from './topology.js';
import {loadEvents, renderEvents} from './events.js';
import {openPanel, renderPanel} from './panels.js';
import {forgetDiscovery, renderSettings, repaintSec, secs} from './settings.js';
import {renderSetup} from './setup.js';
import {logout, openPw} from './account.js';

export function renderAll() { renderGlobal(); renderTopology(); renderEvents(); renderSettings(); renderSetup(); renderPanel(); }

export let pageVersion = '';
export async function refresh() {
  try {
    const r = toLogin(await fetch('api/status', {cache: 'no-store'}));
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    const first = !st;
    setStatus(await r.json());
    // the agent was updated: this page is its old one, load the new
    if (pageVersion && st.version !== pageVersion) { location.reload(); return; }
    pageVersion = st.version;
    setLastOk(Date.now());
    const live = $('live');
    live.classList.remove('ping');
    void live.offsetWidth; // restart the ripple
    live.classList.add('ping');
    renderAll();
    if (first) {
      $('maintMin').value = st.default_expiry_min || 60;
      if (st.setup_pending && !location.hash) location.hash = '#/assistente'; // a new install starts with the guide
      route();
    }
  } catch (e) {
    setLastOk(lastOk || -1);
  }
  tickLive();
}

export function tickLive() {
  const live = $('live');
  const s = Math.round((Date.now() - lastOk) / 1000);
  const stale = lastOk <= 0 || s > 20;
  live.classList.toggle('stale', stale);
  $('liveText').textContent = lastOk <= 0 ? 'Sem ligação ao agente' : stale ? `Sem resposta há ${s} s` : `Atualizado há ${s} s`;
  renderHud();
}

document.addEventListener('click', async e => {
  if (e.target.closest('[data-mirror-seen]')) {
    forgetDiscovery(); // a new folder: what was discovered is stale
    try {
      toLogin(await fetch('api/mirror/seen', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: '{}'}));
    } catch {} // the hint comes back with the next status; nothing lost
    refresh();
  }
  const open = e.target.closest('[data-panel]');
  if (open) { openPanel(open.dataset.panel, open.dataset.svc, open.dataset.prefill); return; }
  if (e.target.closest('[data-pw]')) { openPw(); return; }
  if (e.target.closest('[data-logout]')) { logout(); return; }
  if (e.target.closest('button[data-scan]')) { post('api/images', {}, 'A verificar as imagens no TNAS', 'images'); return; }
  const b = e.target.closest('button[data-act]');
  if (b) forceAction(b.dataset.svc, b.dataset.act);
});
// Forçar failover / regresso, from the panel's buttons or a drag between the boxes.
export async function forceAction(svc, act) {
  const fail = act === 'failover';
  const yes = await confirmAction(
    fail ? `Forçar o failover de ${svc}?` : `Forçar o regresso de ${svc}?`,
    fail ? 'Cria um snapshot do espelho, arranca a cópia no TNAS e aponta o nome para o TNAS. Acontece mesmo que o serviço responda no servidor.'
      : 'Repõe o DNS, remove a cópia do TNAS e apaga o snapshot. O que foi escrito na cópia perde-se.',
    fail ? 'Forçar failover' : 'Forçar regresso');
  if (yes) post('api/action', {service: svc, action: act}, fail ? `Failover de ${svc} forçado` : `Regresso de ${svc} forçado`, `${act}:${svc}`);
  return yes;
}


document.addEventListener('change', async e => {
  const sel = e.target.closest('select[data-min]');
  if (sel) {
    const v = Number(sel.value), field = sel.dataset.min, name = sel.dataset.svc;
    post('api/config', {services: [{name, [field]: v}]}, 'Guardado', `${field}:${name}`, v);
    return;
  }
  if (e.target.name === 'mode') {
    const mode = e.target.value;
    if (mode === 'auto' && !await confirmAction('Passar para automático?',
      'O agente passa a fazer failover e regresso sozinho, sem pedir confirmação.', 'Passar para automático')) {
      e.target.blur();
      repaintSec('geral'); // put the radio back on what the agent has
      return;
    }
    post('api/config', {mode}, mode === 'auto' ? 'Modo automático ligado' : 'Modo observação ligado', 'mode', mode);
    return;
  }
  if (e.target.id === 'interval') {
    const v = Number(e.target.value);
    post('api/config', {check_interval_s: v}, `Verificação a cada ${secs(v)}`, 'interval', v);
    return;
  }
  const sw = e.target.closest('input.switch');
  if (!sw) return;
  const svc = sw.id === 'maintGlobal' ? '' : sw.dataset.maint;
  const minutes = sw.checked ? Number($('maintMin').value) : 0;
  const what = svc ? `Manutenção de ${svc}` : 'Manutenção global';
  post('api/maintenance', {service: svc, minutes}, minutes ? `${what} ligada por ${dur(minutes)}` : `${what} desligada`, `maint:${svc}`, sw.checked);
});


// The page is in the address (#/definicoes/dns, #/assistente), so a reload or a
// link from elsewhere opens it and the browser's back button moves between them.
// An old #/eventos opens the overview, where the events now are.
export const ROUTES = {'': 'overview', eventos: 'overview', definicoes: 'settings', assistente: 'setup'};
export const hashParts = () => location.hash.replace(/^#\/?/, '').split('/');
export const currentTab = () => ROUTES[hashParts()[0]] || 'overview';
export function route() {
  const tab = currentTab(), sub = hashParts()[1] || 'geral';
  document.querySelectorAll('.page').forEach(p => { p.hidden = p.id !== `page-${tab}`; });
  document.querySelectorAll('[data-sub]').forEach(a => tab === 'settings' && a.dataset.sub === sub ? a.setAttribute('aria-current', 'true') : a.removeAttribute('aria-current'));
  if (tab === 'overview') loadEvents();
  if (st) renderAll();
  if (tab === 'settings') $(`set-${sub}`)?.scrollIntoView({block: 'start'});
  if (tab === 'overview') requestAnimationFrame(() => st && renderWires()); // it had no size while hidden
}
addEventListener('hashchange', route);
route();

new ResizeObserver(() => st && renderWires()).observe($('topo'));
// Nothing is asked while the page is hidden (another tab, the phone locked);
// back in view it asks at once.
refresh();
setInterval(() => { if (!document.hidden) refresh(); }, 5000);
setInterval(() => { if (!document.hidden) tickLive(); }, 1000);
addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
