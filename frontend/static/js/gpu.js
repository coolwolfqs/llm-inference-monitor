// gpu.js - GPU Rendering (Cards / Details / Power Tabs)
var _selectedGpuIndex = parseInt(localStorage.getItem('dash_gpu_index') || '0');
window._realGpusForPwr = [];

function gpuFmtNum(v, digits, suffix) {
    if (v === undefined || v === null || v === '') return '--';
    var n = Number(v);
    return isFinite(n) ? n.toFixed(digits == null ? 1 : digits) + (suffix || '') : '--';
}

function renderGpuCard(gpu, idx) {
    var shortName = (gpu.name || 'GPU ' + idx).replace('NVIDIA GeForce RTX ', 'RTX ');
    var memUsedGB = gpu.mem_used ? (Number(gpu.mem_used) / 1024).toFixed(1) : '--';
    var memTotalGB = gpu.mem_total ? (Number(gpu.mem_total) / 1024).toFixed(0) : '--';
    var memPct = gpu.mem_util_pct != null ? parseFloat(Number(gpu.mem_util_pct).toFixed(1)) : null;
    var util = gpu.util != null ? parseFloat(Number(gpu.util).toFixed(1)) : null;
    var fan = (gpu.fan_speed != null) ? gpu.fan_speed.toFixed(0) + '%' : '--';
    var clock = gpu.clock ? Math.round(gpu.clock) + ' MHz' : '--';
    var pwrDraw = gpu.power_draw != null ? gpu.power_draw.toFixed(1) + ' W' : '--';
    var pwrLimit = gpu.power_limit ? gpu.power_limit.toFixed(0) + ' W' : '--';
    var pwrPct = (gpu.power_limit > 0) ? Math.round(gpu.power_draw / gpu.power_limit * 100) : 0;
    var temp = gpu.temp != null ? gpu.temp + '\u00b0C' : '--';
    var memColor = memPct === null ? 'var(--text3)' : pctColor(memPct);
    var utilColor = util === null ? 'var(--text3)' : pctColor(util);
    var tempColor = pctColor(gpu.temp || 0);

    function meter(label, value, pct, color) {
        var safePct = Math.max(0, Math.min(100, pct || 0));
        return '<div class="gpu-detail-meter">' +
            '<div class="gpu-detail-meter-head"><span>' + label + '</span><b style="color:' + color + '">' + value + '</b></div>' +
            '<div class="gpu-detail-track"><span style="width:' + safePct + '%;background:' + color + '"></span></div></div>';
    }
    function meta(label, value, extraClass) {
        return '<div class="gpu-detail-meta-item"><span>' + label + '</span><b class="' + (extraClass || '') + '">' + escHtml(value) + '</b></div>';
    }

    var pwrColor = pctColor(pwrPct);
    var html = '<div class="gpu-detail-card">' +
        '<div class="gpu-detail-head"><div><span class="gpu-detail-kicker">GPU ' + idx + '</span><b>' + escHtml(shortName) + '</b></div>' +
        '<span class="gpu-detail-badge">' + (util === null ? '--' : util.toFixed(1) + '%') + '</span></div>' +
        '<div class="gpu-detail-body">' +
        '<div class="gpu-detail-meters">' +
        meter('GPU Util', util === null ? '--' : util.toFixed(1) + '%', util || 0, utilColor) +
        meter('VRAM', memUsedGB + '/' + memTotalGB + ' GB (' + (memPct === null ? '--' : memPct.toFixed(1) + '%') + ')', memPct || 0, memColor) +
        meter('Power', pwrDraw + '/' + pwrLimit + ' (' + pwrPct + '%)', pwrPct, pwrColor) +
        meter('Temp', temp + '', Math.min(gpu.temp || 0, 100), tempColor) +
        '</div>' +
        '<div class="gpu-detail-meta-grid">' +
        meta('Fan', fan) + meta('Clock', clock) + meta('Available VRAM', gpu.mem_free ? (Number(gpu.mem_free) / 1024).toFixed(1) + ' GB' : '--') +
        meta('Memory Type', gpu.mem_type || '--') + meta('Encoder', gpu.enc_util != null ? Number(gpu.enc_util).toFixed(0) + '%' : '--') +
        meta('Decoder', gpu.dec_util != null ? Number(gpu.dec_util).toFixed(0) + '%' : '--') +
        '</div></div></div>';
    return html;
}

function renderGpuCards(gpus) {
    var grid = Utils.el('gpu-cards-grid');
    if (!grid) return;
    if (!gpus || gpus.length === 0) {
        grid.innerHTML = '<div style="text-align:center;padding:20px;color:var(--text3)">No GPUs detected</div>';
        return;
    }
    var cols = Math.min(gpus.length, 2);
    grid.style.gridTemplateColumns = 'repeat(' + cols + ', 1fr)';
    var html = '';
    for (var i = 0; i < gpus.length; i++) {
        html += renderGpuCard(gpus[i], i);
    }
    grid.innerHTML = html;
}

function renderGpuPwrTabs(realGpus) {
    var tabsContainer = Utils.el('gpu-pwr-tabs');
    if (!tabsContainer || !realGpus || realGpus.length === 0) return;
    var html = '';
    for (var i = 0; i < realGpus.length; i++) {
        var active = i === _selectedGpuIndex ? ' active' : '';
        html += '<button class="pwr-tab' + active + '" data-idx="' + i + '" onclick="selectGpuTab(' + i + ')">GPU ' + i + '</button>';
    }
    tabsContainer.innerHTML = html;
}

function selectGpuTab(idx) {
    _selectedGpuIndex = idx;
    localStorage.setItem('dash_gpu_index', String(idx));
    document.querySelectorAll('.pwr-tab').forEach(function(t) { t.classList.remove('active'); });
    var tab = document.querySelector('.pwr-tab[data-idx="' + idx + '"]');
    if (tab) tab.classList.add('active');
}
