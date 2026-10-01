// The password dialog, the idle logout and logout.
import {$, cap, readErr, st, toLogin, toast} from './core.js';
import {refresh} from './main.js';

export function openPw() {
  $('pwForm').reset();
  $('pwUser').value = st?.user || 'admin';
  $('pwErr').textContent = '';
  $('pwDlg').showModal();
}
// Out after 30 min without input. The page's polling keeps the session alive on
// the agent, so idleness is counted here, across tabs through localStorage.
export const IDLE_MS = 30 * 60e3;
export let lastInput = Date.now();
export const touch = () => { lastInput = Date.now(); try { localStorage.failoverInput = lastInput; } catch {} };
touch();
['pointerdown', 'keydown', 'wheel'].forEach(t => addEventListener(t, touch, {passive: true}));
setInterval(() => {
  let t = lastInput;
  try { t = Math.max(t, Number(localStorage.failoverInput) || 0); } catch {}
  if (Date.now() - t > IDLE_MS) logout();
}, 10e3);
export async function logout() {
  await fetch('api/logout', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: '{}'});
  location.href = 'login';
}
$('pwCancel').addEventListener('click', () => $('pwDlg').close());
$('pwForm').addEventListener('submit', async e => {
  e.preventDefault();
  if ($('pwNew').value !== $('pwAgain').value) { $('pwErr').textContent = 'As duas novas passwords não são iguais.'; return; }
  $('pwSave').disabled = true;
  try {
    const r = toLogin(await fetch('api/password', {method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({current: $('pwCurrent').value, new: $('pwNew').value})}));
    if (!r.ok) throw new Error((await readErr(r)).error);
    $('pwDlg').close();
    toast('Password alterada');
    refresh();
  } catch (err) {
    $('pwErr').textContent = cap(err.message) + '.';
  } finally {
    $('pwSave').disabled = false;
  }
});
