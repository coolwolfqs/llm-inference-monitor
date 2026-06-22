// monitor.js - Main: Data Fetch, SSE, Incremental DOM Updates

// --- Incremental DOM helpers ---
function _t(id) { return document.getElementById(id); }

function setText(id, val, fmt) {
    var el = _t(id);
    if (!el) return;
    var s = fmt ? fmt(val) : (val == null ? '--' : String(val));
    if (el.__t !== s) { el.textContent = s; el.__t = s; }
}

function setHtml(id, h) {
    var el = _t(id);
    if (!el) return;
    if (el.__h !== h) { el.innerHTML = h; el.__h = h; }
}

function setStyle(id, prop, val) {
    var el = _t(id);
    if (!el) return;
    if (el['_s' + prop] !== val) { el.style[prop] = val; el['_s' + prop] = val; }
}

function setBar(id, pct) {
    setStyle(id, 'width', Math.min(Math.max(pct || 0, 0), 100) + '%');
}

// --- History Buffer ---
var _history = {};

function getHist() { return _history; }

function seedHist(h) { if (h) _history = h; }

function pushHist(key, val) {
    if (!_history[key]) _history[key] = [];
    _history[key].push(val);
    if (_history[key].length > 120) _history[key].shift();
}

// --- GPU history by index ---
var _gpuHist = {};

function pushGpuHist(key, idx, val) {
    var k = key + '_' + idx;
    if (!_gpuHist[k]) _gpuHist[k] = [];
    _gpuHist[k].push(val);
    if (_gpuHist[k].length > 120) _gpuHist[k].shift();
}

// --- Render Service Validation ---
function renderServiceValidation(d, g, realGpus) {
    var el = _t('svc-validation-grid');
    if (!el) return;
    realGpus = realGpus || [];
    var services = d.services || {};
    var metrics = d.metrics || {};
    var llm = d.llm_metrics || {};
    var kv = d.kv_cache || {};
    var kvSummary = kv.summary || {};
    var cards = kv.cards || [];
    var inference = d.inference_stats || {};

    var llamaSvc = services['推理服务'] || services['inference'] || {};
    var gpuProcs = [];
    realGpus.forEach(function(gpu) {
        (gpu.processes || []).forEach(function(p) {
            var name = String(p.name || '');
            if (name.indexOf('llama') >= 0 && gpuProcs.indexOf(String(p.pid)) < 0) {
                gpuProcs.push(String(p.pid));
            }
        });
    });

    function card(level, title, main, detail) {
        return '<div class="svc-check-card ' + level + '"><div class="svc-check-head"><span></span><b>' + escHtml(title) + '</b></div>' +
            '<div class="svc-check-main">' + escHtml(main) + '</div><div class="svc-check-detail">' + escHtml(detail || '') + '</div></div>';
    }

    function sum(arr, key) { return arr.reduce(function(a, item) { return a + Number(item[key] || 0); }, 0); }

    var cardsUsed = sum(cards, 'used_mb');
    var gpuUsed = sum(realGpus, 'mem_used');
    var gpuTotal = sum(realGpus, 'mem_total');
    var avgUtil = realGpus.length ? sum(realGpus, 'util') / realGpus.length : Number(g.util || 0);
    var memDiff = Math.abs(gpuUsed - Number(g.mem_used || 0));
    var cardDiff = Math.abs(cardsUsed - gpuUsed);
    var activeReq = Number(metrics.requests_processing || inference.active_slots || 0);
    var genTps = Number(metrics.gen_tps_now || metrics.gen_tps || llm.gen_tps || 0);

    var processLevel = (llamaSvc.status === 'running' && gpuProcs.length) ? 'ok' : (llamaSvc.status === 'running' ? 'warn' : 'bad');
    var gpuLevel = (realGpus.length <= 1 || memDiff <= 256) ? 'ok' : 'warn';
    var metricLevel = activeReq > 0 && genTps > 0 ? 'ok' : ((metrics.available || llm.available) ? 'warn' : 'bad');
    var kvLevel = kvSummary.captured === false ? 'warn' : (cardDiff <= 768 ? 'ok' : 'warn');

    el.innerHTML =
        card(processLevel, 'Process', gpuProcs.length ? ('PID ' + gpuProcs.join(', ')) : 'No process confirmed', 'Status=' + (llamaSvc.status || '--')) +
        card(gpuLevel, 'GPU', realGpus.length + ' GPU(s), avg util ' + (avgUtil).toFixed(1) + '%', 'VRAM ' + (gpuUsed / 1024).toFixed(1) + '/' + (gpuTotal / 1024).toFixed(1) + ' GB') +
        card(metricLevel, 'Inference', activeReq > 0 ? ('Running, ' + genTps.toFixed(1) + ' tok/s') : 'No active generation', 'active=' + activeReq) +
        card(kvLevel, 'KV / VRAM', 'KV ' + ((kvSummary.kv_total_mb || 0) / 1024).toFixed(1) + ' GB', 'Delta ' + cardDiff.toFixed(0) + ' MB');
}

// --- Engine Cards ---
async function renderEngines() {
    try {
        var r = await fetch('/api/engines');
        var data = await r.json();
        var engines = (data.engines || []).filter(function(e) { return e.key !== 'vllm'; });
        setText('engine-count', engines.length + ' engines');
        var container = _t('engine-cards');
        if (!container) return;
        var cacheKey = engines.map(function(e) { return e.key + '_' + (e.is_running ? 1 : 0); }).join(',');
        if (container.__ck === cacheKey) return;
        container.__ck = cacheKey;
        var html = '';
        engines.forEach(function(eng) {
            var act = eng.is_running;
            var expanded = localStorage.getItem('eng_expand_' + eng.key) === 'true';
            html += '<div class="engine-card' + (act ? ' engine-active' : '') + '" data-key="' + escHtml(eng.key) + '">' +
                '<div class="engine-card-header" data-engine-key="' + escHtml(eng.key) + '" onclick="toggleEngineCard(this.dataset.engineKey)">' +
                '<div class="engine-card-left"><span class="engine-status-dot' + (act ? ' active' : '') + '"></span><span class="engine-key">' + escHtml(eng.key) + '</span>' +
                (act ? '<span class="engine-badge">Active</span>' : '') + '</div>' +
                '<span class="engine-expand-icon' + (expanded ? ' expanded' : '') + '">\u25bc</span></div>' +
                '<div class="engine-card-body' + (expanded ? ' show' : '') + '" id="engine-detail-' + escHtml(eng.key) + '">' +
                '<div class="engine-detail-row"><span class="engine-detail-label">Version</span><span class="engine-detail-value">' + escHtml(eng.display_version || eng.version || '-') + '</span></div>' +
                '<div class="engine-detail-row"><span class="engine-detail-label">GitHub</span><span class="engine-detail-value">' +
                (eng.github_url ? '<a href="' + escHtml(eng.github_url) + '" target="_blank" style="color:var(--accent);text-decoration:none">' + escHtml(eng.upstream_tag || 'source') + '</a>' : '<span style="color:var(--text3)">-</span>') + '</span></div>' +
                '<div class="engine-detail-row"><span class="engine-detail-label">Status</span><span class="engine-detail-value">' + (act ? 'Running' : 'Stopped') + '</span></div>' +
                '<div style="margin-top:8px;font-size:11px;color:var(--text3);">Features</div>' +
                '<div class="engine-features">' + (eng.features || []).map(function(f) { return '<span class="feature-tag">' + escHtml(f) + '</span>'; }).join(' ') + '</div>' +
                '</div>' +
                (act ? '' : '<div class="engine-card-actions" style="padding:4px 10px"><button class="switch-btn-sm" data-engine-key="' + escHtml(eng.key) + '" onclick="switchEngine(this.dataset.engineKey)">Switch Here</button></div>') +
                '</div>';
        });
        container.innerHTML = html || '<div style="text-align:center;padding:32px;color:var(--text3)">No engines available</div>';
    } catch(e) {}
}

function toggleEngineCard(key) {
    var det = _t('engine-detail-' + key);
    if (!det) return;
    var show = !det.classList.contains('show');
    det.classList.toggle('show', show);
    localStorage.setItem('eng_expand_' + key, show);
}

async function switchEngine(key) {
    if (!confirm('Switch to ' + key + '? Inference will restart.')) return;
    showToast('Switching engine: ' + key);
    try {
        var r = await fetch('/api/engine/switch', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ engine: key }) });
        var d = await r.json();
        if (d.error) showToast('\u274c ' + d.error);
        else if (d.active) { showToast('\u2705 Switched'); fetchStatus(); renderEngines(); }
        else showToast('\u274c Switch failed');
    } catch(e) { showToast('\u274c ' + e.message); }
}

// --- Render model load and params ---
function renderServiceLoadAndParams(d, g) {
    var cfg = (d.services && d.services['inference']) || {};
    var deploy = d.deploy_config || {};
    var params = d.params || {};
    var merged = {};
    [deploy, params, cfg.config || {}].forEach(function(src) { if (src) { for (var k in src) merged[k] = src[k]; } });

    function first() { for (var i = 0; i < arguments.length; i++) { var v = arguments[i]; if (v !== undefined && v !== null && v !== '' && v !== '--' && v !== 'N/A') return v; } return '--'; }
    function chip(label, value, cls) { return '<div class="svc-load-chip ' + (cls || '') + '"><span>' + label + '</span><b>' + escHtml(String(first(value))) + '</b></div>'; }
    function paramChip(label, key) { var v = first(merged[key], cfg[key]); if (v === '--') return ''; if (typeof v === 'boolean') v = v ? 'On' : 'Off'; return '<span class="svc-param-chip"><i>' + label + '</i><b>' + escHtml(String(v)) + '</b></span>'; }

    var modelFile = first(cfg.model_file, deploy.model, d.model, d.current_model);
    var loadEl = _t('svc-load-summary');
    if (loadEl) {
        loadEl.innerHTML = '<div class="svc-load-model"><span>Current Model</span><b title="' + escHtml(String(modelFile)) + '">' + escHtml(String(modelFile).split('/').pop()) + '</b></div>' +
            '<div class="svc-load-grid">' +
            chip('Context', first(merged.ctx_size, cfg.ctx, cfg.context, deploy.ctx_size), 'accent') +
            chip('Layers', first(merged.ngl, merged.n_gpu_layers, cfg.ngl), 'accent') +
            chip('Port', first(cfg.port, deploy.port, 8080)) +
            '</div>';
    }

    var paramEl = _t('svc-param-groups');
    if (paramEl) {
        var groups = [
            ['Resources', ['gpu', 'concurrency', 'threads']],
            ['Batching', ['batch', 'ubatch']],
            ['KV Cache', ['k_cache_type', 'v_cache_type', 'kv_offload']],
            ['Inference', ['temp', 'flash_attn', 'spec_draft_n_max', 'reasoning']],
        ];
        var html = '';
        groups.forEach(function(grp) {
            var chips = grp[1].map(function(k) { return paramChip(k, k); }).filter(Boolean).join('');
            if (chips) html += '<div class="svc-param-group"><div class="svc-param-title">' + grp[0] + '</div><div class="svc-param-list">' + chips + '</div></div>';
        });
        paramEl.innerHTML = html || '<div class="svc-empty">No deployment params</div>';
    }
}

// ================================================================
// updateDashboard - Main rendering entry point
// ================================================================
var _lastData = null;

function updateDashboard(d) {
    _lastData = d;
    var gpuAgg = d.gpu_aggregate || (d.gpus && d.gpus.aggregate) || {};
    var gpus = (d.gpus && d.gpus.gpus) || [];
    var g = gpuAgg;
    var cpu = d.cpu || {};
    var mem = d.memory || {};
    var inf = d.inference_stats || {};

    // --- GPU Mini Card ---
    var gpu0 = gpus[0] || {};
    setText('gpu-card-name', (gpu0.name || '--').replace('NVIDIA ', 'NV '));
    setText('gpu-util', (gpu0.util != null ? gpu0.util.toFixed(0) : g.util || 0) + '%');
    setBar('gpu-util-bar', gpu0.util != null ? gpu0.util : (g.util || 0));
    var mu = gpu0.mem_used != null ? gpu0.mem_used : (g.mem_used || 0);
    var mt = gpu0.mem_total != null ? gpu0.mem_total : (g.mem_total || 1);
    setText('gpu-mem', (mu / 1024).toFixed(1) + '/' + (mt / 1024).toFixed(0) + ' GB');
    setBar('gpu-mem-bar', (mu / Math.max(mt, 1)) * 100);
    setText('gpu-power', (gpu0.power_draw || g.power_draw || 0).toFixed(0) + '/' + (gpu0.power_limit || 0).toFixed(0) + ' W');
    setBar('gpu-power-bar', g.power_limit > 0 ? (g.power_draw / g.power_limit * 100) : 0);
    setText('gpu-temp', (gpu0.temp || g.temp || 0).toFixed(0) + '\u00b0C');
    setBar('gpu-temp-bar', gpu0.temp || g.temp || 0);
    setText('gpu-fan', (gpu0.fan_speed != null ? gpu0.fan_speed.toFixed(0) : '--') + '%');
    setBar('gpu-fan-bar', gpu0.fan_speed || 0);
    setText('gpu-clock', (gpu0.clock || '--') + ' MHz');

    // --- System Mini Card ---
    setText('sys-cpu', cpu.usage != null ? cpu.usage.toFixed(1) : '0');
    setText('sys-mem', mem.percent != null ? mem.percent.toFixed(1) : '0');
    setText('sys-cpu-temp', cpu.temp_tctl != null ? cpu.temp_tctl.toFixed(0) : '--');
    setText('sys-disk-temp', d.nvme_temp || 0);
    if (cpu.load1 != null) setText('sys-load', cpu.load1.toFixed(2) + ' / ' + (cpu.load5 || 0).toFixed(2) + ' / ' + (cpu.load15 || 0).toFixed(2));
    if (mem.swap_pct != null) setText('sys-swap', mem.swap_pct.toFixed(1));

    // --- CPU Cores ---
    var coreGrid = _t('core-grid');
    if (coreGrid && cpu.per_core && cpu.per_core.length > 0) {
        var n = cpu.per_core.length;
        var gridClass = 'core-grid';
        if (n <= 8) gridClass += ' c8';
        else if (n <= 16) gridClass += ' c16';
        else gridClass += ' c32';
        coreGrid.className = gridClass;
        var html = '';
        for (var i = 0; i < n; i++) {
            var v = cpu.per_core[i] || 0;
            var color = pctColor(v);
            html += '<div class="core-cell"><div class="core-label">' + i + '</div><div class="core-bar"><div class="core-fill" style="width:' + v + '%;background:' + color + '"></div></div></div>';
        }
        setHtml('core-cores', html);
        // Fix: core-grid uses innerHTML directly via our setHtml
        coreGrid.innerHTML = html;
    }

    // --- Charts ---
    var hist = d.history || _history;
    if (typeof drawSimpleChart === 'function') {
        drawSimpleChart('gpu-util-chart', hist.gpu_util, '#58a6ff', 100);
        drawSimpleChart('gpu-mem-chart', hist.gpu_mem_pct, '#3fb950', 100);
        drawSimpleChart('temp-pwr-chart', hist.gpu_temp, '#f85149', 100);
        drawSimpleChart('cpu-util-chart', hist.cpu_usage, '#3fb950', 100);
        drawSimpleChart('cpu-freq-chart', hist.cpu_freq, '#58a6ff', Math.max.apply(null, hist.cpu_freq) || 5000);
        drawSimpleChart('mem-util-chart', hist.mem_usage, '#3fb950', 100);
        drawSimpleChart('mem-used-chart', hist.mem_used_gb, '#58a6ff', 64);
        drawSimpleChart('disk-active-chart', hist.disk_active, '#3fb950', 100);
        drawSimpleChart('disk-read-chart', hist.disk_read, '#58a6ff', 1073741824);
        drawSimpleChart('disk-write-chart', hist.disk_write, '#3fb950', 1073741824);
        drawSimpleChart('net-chart', hist.net_recv, '#3fb950', 1073741824);
    }

    // GPU + Memory + Disk detail values
    setText('gpu-util-val', g.util != null ? g.util.toFixed(0) : '0');
    setText('gpu-mem-val', g.mem_util_pct != null ? g.mem_util_pct.toFixed(0) : '0');
    setText('gpu-mem-used', (g.mem_used || 0).toFixed(0) + ' MB');
    setText('gpu-mem-total', (g.mem_total || 0).toFixed(0) + ' MB');
    setText('gpu-mem-free', ((g.mem_total || 0) - (g.mem_used || 0)).toFixed(0) + ' MB');
    setText('gpu-freq', gpu0.clock ? Math.round(gpu0.clock) + ' MHz' : '--');
    setText('gpu-freq-max', gpu0.clock_max ? Math.round(gpu0.clock_max) + ' MHz' : '--');
    setText('temp-pwr-temp-val', (gpu0.temp || g.temp || 0).toFixed(0));
    setText('temp-pwr-pwr-val', (gpu0.power_draw || g.power_draw || 0).toFixed(0));

    setText('cpu-util-val', cpu.usage != null ? cpu.usage.toFixed(1) : '0');
    setText('cpu-freq-val', cpu.freq_current != null ? cpu.freq_current.toFixed(0) : '0');
    setText('cpu-freq-now', cpu.freq_current != null ? cpu.freq_current.toFixed(0) + ' MHz' : '--');
    setText('cpu-freq-max', cpu.max_mhz != null ? cpu.max_mhz.toFixed(0) + ' MHz' : '--');
    if (cpu.temp_tctl) setText('cpu-temp-badge', '\ud83c\udf21 ' + cpu.temp_tctl.toFixed(0) + '\u00b0C');

    setText('mem-util-val', mem.percent != null ? mem.percent.toFixed(1) : '0');
    setText('mem-used-val', mem.used_str || '0 GB');
    setText('mem-used', mem.used_str || '0 GB');
    setText('mem-free', mem.free_str || '0 GB');
    setText('mem-total-header', 'Total ' + (mem.total_str || '--'));

    // Memory detail
    setText('mem-info-total', mem.total_str || '--');
    setText('mem-info-used', mem.used_str || '--');
    setText('mem-info-free', mem.free_str || '--');
    setText('mem-info-cached', mem.cached ? fmtSize(mem.cached) : '--');
    setText('mem-info-buffers', mem.buffers ? fmtSize(mem.buffers) : '--');
    setText('mem-cached-val', (mem.cached || 0) > 0 ? fmtSize(mem.cached) : '--');
    setText('mem-swap-used', mem.swap_used ? fmtSize(mem.swap_used) : '--');
    setText('mem-swap-total', mem.swap_total ? fmtSize(mem.swap_total) : '--');
    setText('mem-swap-pct', mem.swap_pct != null ? mem.swap_pct.toFixed(1) + '%' : '--');

    // Disk detail
    setText('disk-active-val', (d.disk_io ? d.disk_io.active_pct : (d.system ? d.system.disk_active_pct : 0)) || 0);
    setText('disk-read', (d.disk_io ? d.disk_io.read_str : '0 B/s'));
    setText('disk-write', (d.disk_io ? d.disk_io.write_str : '0 B/s'));
    setText('disk-read-val', (d.disk_io ? d.disk_io.read_str : '0 B/s'));
    setText('disk-write-val', (d.disk_io ? d.disk_io.write_str : '0 B/s'));
    if (d.disk_model) {
        setText('disk-info-model', d.disk_model.model || '--');
        setText('disk-info-type', d.disk_model.type || '--');
        setText('disk-info-size', d.disk_model.size ? d.disk_model.size.toFixed(1) + ' GB' : '--');
    }
    setText('disk-info-temp', d.nvme_temp ? d.nvme_temp + '\u00b0C' : '--');
    setText('disk-temp-header', 'Temp ' + (d.nvme_temp ? d.nvme_temp + '\u00b0C' : '--'));

    // Partitions
    if (d.disks) {
        var parts = [];
        for (var pk in d.disks) {
            var pd = d.disks[pk];
            parts.push(pd);
        }
        if (parts.length > 0) {
            var ph = '';
            parts.forEach(function(p) {
                var color = pctColor(p.percent || 0);
                ph += '<div style="margin:4px 0;font-size:10px"><span style="color:var(--text3)">' + escHtml(p.mount || '') + '</span> <b style="color:' + color + '">' + (p.percent || 0).toFixed(0) + '%</b></div>';
            });
            setHtml('disk-partitions', ph);
        }
    }

    // Network detail
    if (d.network) {
        setText('net-adapter-name', d.network.adapter || 'eth0');
        setText('net-adapter-detail', d.network.vendor || '--');
        setText('net-speed', d.network.link_speed || '--');
        setText('net-ipv4', d.network.ipv4 || '--');
        setText('net-recv', d.network.recv_str || '0 B/s');
        setText('net-sent', d.network.sent_str || '0 B/s');
    }

    // CPU detail panel
    if (cpu.physical_cores) setText('cpu-info-cores', cpu.physical_cores + 'C / ' + (cpu.logical_cores || '--') + 'T');
    if (cpu.virt) setText('cpu-info-virt', cpu.virt);
    if (cpu.l2_cache) setText('cpu-info-l2', cpu.l2_cache);
    if (cpu.l3_cache) setText('cpu-info-l3', cpu.l3_cache);
    if (cpu.temp_tctl) setText('cpu-info-temp', cpu.temp_tctl.toFixed(0) + '\u00b0C');
    if (cpu.load1 != null) setText('cpu-info-load', cpu.load1.toFixed(2) + ' / ' + (cpu.load5 || 0).toFixed(2) + ' / ' + (cpu.load15 || 0).toFixed(2));
    if (cpu.process_count != null) setText('cpu-proc-count', cpu.process_count);
    setText('cpu-cores-header', (cpu.logical_cores || '--') + ' logical processors');

    // GPU detail panel
    setText('gpu-section-title', 'GPU' + (gpus.length > 0 ? ' (' + gpus.length + ')' : ''));
    if (gpu0.arch) setText('gpu-info-arch', gpu0.arch);
    if (gpu0.mem_type) setText('gpu-info-memtype', gpu0.mem_type);
    if (gpu0.bus_width) setText('gpu-info-buswidth', gpu0.bus_width);
    if (gpu0.cuda_cores) setText('gpu-info-cuda', gpu0.cuda_cores.toLocaleString());
    var tdp = 'N/A';
    if (gpu0.tdp_min && gpu0.tdp_max) tdp = gpu0.tdp_min.toFixed(0) + 'W - ' + gpu0.tdp_max.toFixed(0) + 'W';
    else if (gpu0.power_limit) tdp = gpu0.power_limit.toFixed(0) + 'W';
    setText('gpu-info-tdp', tdp);

    // GPU processes table
    var procsEl = _t('gpu-procs');
    if (procsEl && gpu0.processes && gpu0.processes.length > 0) {
        var ph = '';
        gpu0.processes.forEach(function(p) {
            ph += '<tr><td style="text-align:left">' + (p.pid || '-') + '</td><td>' + escHtml(p.name || '--') + '</td><td style="text-align:center;color:var(--text3)">-</td><td style="text-align:right">' + (p.used_memory ? (Number(p.used_memory) / 1024).toFixed(0) + ' MB' : '--') + '</td></tr>';
        });
        procsEl.innerHTML = ph || '<tr><td colspan="4" class="empty-msg">No processes</td></tr>';
    }

    // Render GPU cards
    if (typeof renderGpuCards === 'function') renderGpuCards(gpus);

    // Service validation
    renderServiceValidation(d, g, gpus);

    // Service load and params
    renderServiceLoadAndParams(d, g);

    // Health score
    var hw = 100, hwi = [];
    if (g.mem_util_pct > 90) { hw -= 30; hwi.push('VRAM critical (' + g.mem_util_pct.toFixed(0) + '%)'); }
    else if (g.mem_util_pct > 80) { hw -= 15; hwi.push('VRAM high (' + g.mem_util_pct.toFixed(0) + '%)'); }
    if ((gpu0.temp || g.temp || 0) > 80) { hw -= 20; hwi.push('GPU hot (' + (gpu0.temp || g.temp || 0) + '\u00b0C)'); }
    else if ((gpu0.temp || g.temp || 0) > 70) { hw -= 10; hwi.push('GPU warm (' + (gpu0.temp || g.temp || 0) + '\u00b0C)'); }
    hw = Math.max(0, hw);
    var ss = 100, si = [];
    if (cpu.usage > 90) { ss -= 30; si.push('CPU full (' + cpu.usage.toFixed(0) + '%)'); }
    else if (cpu.usage > 75) { ss -= 15; si.push('CPU high (' + cpu.usage.toFixed(0) + '%)'); }
    if (mem.percent > 90) { ss -= 30; si.push('Memory critical (' + mem.percent.toFixed(0) + '%)'); }
    else if (mem.percent > 80) { ss -= 10; si.push('Memory high (' + mem.percent.toFixed(0) + '%)'); }
    ss = Math.max(0, ss);
    var isc = 100, isi = [];
    if (inf.queue_depth > 10) { isc -= 20; isi.push('Queue deep (' + inf.queue_depth + ')'); }
    else if (inf.queue_depth > 5) { isc -= 10; isi.push('Queue depth ' + inf.queue_depth); }
    isc = Math.max(0, isc);
    var ov = Math.round((hw + ss + isc) / 3);

    function setHealthCard(id, score, issues) {
        var el = _t(id);
        if (!el) return;
        var b = el.querySelector('b');
        if (b) { var bs = String(score); if (b.__t !== bs) { b.textContent = bs; b.__t = bs; } }
        el.classList.remove('good', 'warn', 'bad');
        var cls = score >= 80 ? 'good' : score >= 50 ? 'warn' : 'bad';
        el.classList.add(cls);
        var bar = el.querySelector('.health-meter span');
        if (bar) { var pct = Math.min(Math.max(score || 0, 0), 100); setStyle(bar.id || (bar.parentElement.parentElement.id + '-bar'), 'width', pct + '%'); }
        var de = el.querySelector('.health-card-detail');
        if (de) { var ds = issues.length > 0 ? issues.join('; ') : 'Healthy'; if (de.__t !== ds) { de.textContent = ds; de.__t = ds; } }
    }
    setHealthCard('health-hardware', hw, hwi);
    setHealthCard('health-system', ss, si);
    setHealthCard('health-inference', isc, isi);
    var hd = _t('health-score-header');
    if (hd) { hd.style.color = ov >= 80 ? 'var(--green)' : ov >= 50 ? 'var(--yellow)' : 'var(--red)'; hd.title = 'Health: ' + ov; }

    // Inference stats
    setText('inf-tps', (inf.last_tps || 0).toFixed(1) + ' tok/s');
    setText('inf-tps-prompt', inf.last_prompt_ms > 0 && inf.last_prompt_tokens > 0 ? (inf.last_prompt_tokens / inf.last_prompt_ms * 1000).toFixed(1) + ' t/s' : '-');
    setText('inf-prompt-tokens', formatTokenNum(inf.last_prompt_tokens));
    setText('inf-eval-tokens', formatTokenNum(inf.last_eval_tokens));
    setText('inf-prompt-time', formatMsShort(inf.last_prompt_ms));
    setText('inf-eval-time', formatMsShort(inf.last_eval_ms));
    setText('inf-slots', (inf.active_slots || 0) + ' / ' + (inf.total_slots || 0));
    setText('inf-queue', inf.queue_depth || 0);

    // ECG
    if (typeof updateEcgTps === 'function') updateEcgTps(inf.last_tps || 0);

    // LLM metrics
    if (typeof renderLlmMetrics === 'function' && d.llm_metrics) renderLlmMetrics(d.llm_metrics);

    // KV Cache
    if (typeof renderKvCache === 'function' && d.kv_cache) renderKvCache(d.kv_cache);

    // IP stats
    if (d.ip_stats && typeof renderIpStats === 'function') renderIpStats(d.ip_stats);

    // Model info
    var svc = d.services && (d.services['inference'] || d.services['推理服务']);
    if (svc && svc.model_file && typeof renderModelInfo === 'function') {
        var cfg = svc.config || d.deploy_config || {};
        renderModelInfo(svc.model_file, cfg);
    }

    // Uptime
    if (d.uptime) setText('uptime-display', 'Running ' + d.uptime);
}

// ================================================================
// SSE Real-time streaming
// ================================================================
var _sse = null;
var _sseOk = false;
var _sseRetry = 0;
var _lastSseTickTs = 0;
var _statusFetchInFlight = false;

function connectSSE() {
    if (_sse) _sse.close();
    _sse = new EventSource('/api/sse');
    _sse.onopen = function() { _sseRetry = 0; _sseOk = false; updateConnStatus(false); };
    _sse.onerror = function() {
        _sseOk = false;
        updateConnStatus(false);
        if (!_sse || _sse.readyState === EventSource.CLOSED) {
            var delay = [5, 10, 20, 40, 60][Math.min(_sseRetry, 4)] * 1000;
            _sseRetry++;
            setTimeout(connectSSE, delay);
        }
    };
    _sse.addEventListener('tick', function(e) {
        try {
            _lastSseTickTs = Date.now();
            if (!_sseOk) { _sseOk = true; updateConnStatus(true); }
            var data = JSON.parse(e.data);
            var agg = data.gpus ? data.gpus.aggregate : null;
            if (agg) {
                pushHist('gpu_util', agg.util || 0);
                pushHist('gpu_mem_pct', agg.mem_util_pct || 0);
                pushHist('gpu_temp', agg.temp || 0);
                pushHist('gpu_power', agg.power_draw || 0);
            }
            if (data.system) {
                pushHist('cpu_usage', data.system.cpu_util || 0);
                pushHist('cpu_freq', data.system.cpu_freq_current || 0);
                if (data.system.mem_used_pct != null) pushHist('mem_usage', data.system.mem_used_pct);
                pushHist('mem_used_gb', (data.system.mem_used || 0) / 1073741824);
                pushHist('disk_active', data.system.disk_active_pct || 0);
                pushHist('disk_read', data.system.disk_read_bps || 0);
                pushHist('disk_write', data.system.disk_write_bps || 0);
                if (data.system.net_adapter) {
                    pushHist('net_recv', data.system.net_recv_bps || 0);
                    pushHist('net_sent', data.system.net_sent_bps || 0);
                }
            }
            // Build flat data
            var flat = {
                gpus: data.gpus ? data.gpus.gpus : [],
                cpu: data.system ? {
                    usage: data.system.cpu_util, model: data.system.cpu_model,
                    freq_current: data.system.cpu_freq_current, max_mhz: data.system.cpu_max_mhz,
                    temp_tctl: data.system.cpu_temp_tctl,
                    physical_cores: data.system.cpu_physical_cores, logical_cores: data.system.cpu_logical_cores,
                    virt: data.system.cpu_virt, l2_cache: data.system.cpu_l2, l3_cache: data.system.cpu_l3,
                    per_core: data.system.cpu_per_core || [],
                    load1: data.system.load_1, load5: data.system.load_5, load15: data.system.load_15,
                    process_count: data.system.process_count
                } : {},
                memory: data.system ? {
                    percent: data.system.mem_used_pct, used: data.system.mem_used,
                    available: data.system.mem_available, total: data.system.mem_total,
                    used_str: data.system.mem_used_str, free_str: data.system.mem_free_str,
                    total_str: data.system.mem_total_str,
                    swap_pct: data.system.swap_used_pct, swap_used: data.system.swap_used,
                    swap_total: data.system.swap_total, cached: data.system.mem_cached,
                    buffers: data.system.mem_buffers
                } : {},
                inference_stats: data.inference_stats || {},
                kv_cache: data.kv_cache || {},
                uptime: data.uptime, history: getHist()
            };
            if (data.system && data.system.disks) {
                flat.disks = {};
                (data.system.disks || []).forEach(function(dd) {
                    var label = dd.mountpoint ? dd.mountpoint.split('/').pop() || 'root' : 'root';
                    flat.disks[label] = { label: label, mount: dd.mountpoint, percent: dd.used_pct, used: dd.used, total: dd.total };
                });
                flat.disk_io = { active_pct: data.system.disk_active_pct || 0, read_str: data.system.disk_read_str, write_str: data.system.disk_write_str };
                flat.disk_model = { model: data.system.disk_model, type: data.system.disk_type, size: data.system.disk_size };
                flat.nvme_temp = data.system.nvme_temp;
            }
            if (data.system && data.system.net_adapter) {
                flat.network = { adapter: data.system.net_adapter, vendor: data.system.net_vendor, link_speed: data.system.net_link_speed, ipv4: data.system.net_ipv4, recv_str: data.system.net_recv_str, sent_str: data.system.net_sent_str };
            }
            if (data.gpus && data.gpus.aggregate) flat.gpu_aggregate = data.gpus.aggregate;
            if (data.history) seedHist(data.history);
            updateDashboard(flat);
        } catch(err) { console.error('[SSE] parse error:', err); }
    });
}

function updateConnStatus(ok) {
    var el = _t('conn-status');
    if (!el) return;
    el.className = ok ? 'conn-status connected' : 'conn-status disconnected';
    el.title = ok ? 'SSE connected' : 'Polling mode';
    setHtml('conn-status', ok ? '<span class="conn-dot on"></span> Live' : '<span class="conn-dot off"></span> Polling');
}

function updateClock() {
    var el = _t('clock');
    if (!el) return;
    var now = new Date();
    var t = String(now.getHours()).padStart(2, '0') + ':' + String(now.getMinutes()).padStart(2, '0') + ':' + String(now.getSeconds()).padStart(2, '0');
    if (el.__t !== t) { el.textContent = t; el.__t = t; }
}

// ================================================================
// Data fetch - REST polling fallback
// ================================================================
async function fetchStatus() {
    if (_statusFetchInFlight) return;
    _statusFetchInFlight = true;
    try {
        var resp = await fetch('/api/status', { cache: 'no-store' });
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        var data = await resp.json();
        if (data.history) seedHist(data.history);
        updateDashboard(data);
        if (typeof renderEngines === 'function') renderEngines();
    } catch(e) { console.error('[fetchStatus]', e.message); }
    finally { _statusFetchInFlight = false; }
}

// ================================================================
// Init
// ================================================================
if (typeof loadPersistMode === 'function') loadPersistMode();
connectSSE();
fetchStatus();
updateClock();
setInterval(updateClock, 5000);

// Fallback polling when SSE disconnected
setInterval(function() {
    if (_sseOk && _lastSseTickTs && Date.now() - _lastSseTickTs > 8000) {
        _sseOk = false;
        updateConnStatus(false);
    }
    if (!_sseOk) fetchStatus();
}, 5000);

// Full refresh every 30s
setInterval(function() { fetchStatus(); }, 30000);
setInterval(renderEngines, 30000);
