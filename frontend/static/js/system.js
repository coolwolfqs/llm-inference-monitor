// system.js - System Control (Theme, Persist, Reboot/Shutdown, Power Limit)
var _persistMode = 'auto';

async function loadPersistMode() {
    try {
        var resp = await fetch('/api/settings/persist');
        var data = await resp.json();
        _persistMode = data.mode || 'auto';
        updatePersistUI();
    } catch(e) { console.warn('Failed to load persist mode:', e); }
}

function updatePersistUI() {
    var dot = Utils.el('p-dot');
    var label = Utils.el('p-label');
    if (dot && label) {
        dot.className = _persistMode === 'manual' ? 'p-dot manual' : 'p-dot';
        label.textContent = _persistMode === 'manual' ? 'Manual' : 'Auto';
    }
}

async function togglePersist() {
    var btn = Utils.el('persist-toggle');
    if (!btn || btn.classList.contains('busy')) return;
    var newMode = _persistMode === 'auto' ? 'manual' : 'auto';
    btn.classList.add('busy');
    try {
        var resp = await fetch('/api/settings/persist', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({mode: newMode})
        });
        var data = await resp.json();
        if (data.status === 'ok') {
            _persistMode = newMode;
            updatePersistUI();
        }
    } catch(e) { console.warn(e); }
    btn.classList.remove('busy');
}

// Theme toggle
function _getLocalHour() {
    var now = new Date();
    return now.getHours();
}

function _isDaytime() {
    var h = _getLocalHour();
    return h >= 7 && h < 19;
}

function _applyTheme(isLight) {
    document.body.classList.toggle('light', isLight);
    var btn = Utils.el('theme-toggle');
    if (btn) btn.textContent = isLight ? '\u2600\ufe0f Day' : '\ud83c\udf19 Night';
}

function toggleTheme() {
    var isLight = document.body.classList.contains('light');
    var newIsLight = !isLight;
    _applyTheme(newIsLight);
    localStorage.setItem('theme', newIsLight ? 'light' : 'dark');
    localStorage.setItem('theme_manual', '1');
}

document.addEventListener('DOMContentLoaded', function() {
    var manual = localStorage.getItem('theme_manual');
    var saved = localStorage.getItem('theme');
    var isLight;
    if (manual) { isLight = saved === 'light'; }
    else { isLight = _isDaytime(); }
    _applyTheme(isLight);
});

// Auto-switch every 5 min
setInterval(function() {
    if (localStorage.getItem('theme_manual')) return;
    var isLight = _isDaytime();
    var currentlyLight = document.body.classList.contains('light');
    if (isLight !== currentlyLight) _applyTheme(isLight);
}, 300000);

// System actions
function rebootSystem() {
    if (!confirm('\u26a0\ufe0f Confirm system reboot?')) return;
    fetch('/api/action/reboot', {method: 'POST'})
        .then(function(r) { return r.json(); })
        .then(function(d) { alert('Rebooting...'); })
        .catch(function(e) { alert('Request failed: ' + e.message); });
}

function shutdownSystem() {
    if (!confirm('\u26a0\ufe0f Confirm shutdown?')) return;
    fetch('/api/action/shutdown', {method: 'POST'})
        .then(function(r) { return r.json(); })
        .then(function(d) { alert('Shutting down...'); })
        .catch(function(e) { alert('Request failed: ' + e.message); });
}

// System dropdown
var _sysMenuOpen = false;
function toggleSysMenu() {
    var m = Utils.el('sys-menu');
    _sysMenuOpen = !_sysMenuOpen;
    m.classList.toggle('show', _sysMenuOpen);
}

document.addEventListener('click', function(e) {
    if (!_sysMenuOpen) return;
    var dd = document.querySelector('.sys-dropdown');
    if (dd && !dd.contains(e.target)) {
        _sysMenuOpen = false;
        Utils.el('sys-menu').classList.remove('show');
    }
});

// GPU Power Limit
function renderPowerButtons(g) {
    var container = Utils.el('pwr-btns-container');
    if (!container) return;
    container.innerHTML = '';
    var tdpMax = g.tdp_max || g.power_limit || g.tdp || 320;
    var tdpMin = g.tdp_min || Math.round(tdpMax * 0.5);
    var colors = ['var(--green)','#86d63b','var(--yellow)','var(--orange)','var(--red)','var(--purple)','var(--cyan)'];
    var pcts = [40,50,60,70,80,90,100];
    var activePct = parseInt(localStorage.getItem('dash_pwr_pct')) || -1;
    for (var i = 0; i < pcts.length; i++) {
        var pct = pcts[i];
        var actual = Math.round(tdpMin + (tdpMax - tdpMin) * (pct - 40) / 60);
        var btn = document.createElement('button');
        btn.className = 'pwr-btn pwr-' + pct;
        btn.setAttribute('data-pct', pct);
        btn.textContent = pct + '% (' + actual + 'W)';
        btn.onclick = (function(p) { return function() { setPowerLimit(p); }; })(pct);
        if (i < colors.length) btn.style.borderColor = colors[i];
        if (pct === activePct) btn.classList.add('active');
        container.appendChild(btn);
    }
}

function setPowerLimit(pct) {
    localStorage.setItem('dash_pwr_pct', String(pct));
    if (typeof showToast === 'function') showToast('Setting power limit: '+pct+'%', 'info');
    var gpuIndices = [0];
    fetch('/api/gpu/power_limit', {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({percentage: pct, gpu_index: 0})
    }).then(function(r) { return r.json(); }).then(function(d) {
        if (d.status === 'ok') {
            document.querySelectorAll('.pwr-btn').forEach(function(btn) {
                btn.classList.toggle('active', parseInt(btn.dataset.pct) === pct);
            });
            if (typeof showToast === 'function') showToast('\u2705 GPU set to '+pct+'%', 'success');
        } else {
            if (typeof showToast === 'function') showToast('\u26a0\ufe0f Failed: '+(d.message||'error'), 'error');
        }
    }).catch(function(e) {
        if (typeof showToast === 'function') showToast('\u274c '+e.message, 'error');
    });
}
