// inference.js - Inference Stats / KV Cache / Request Panel
// KV Cache rendering
var _kv_prevPct = null;
var _kv_trendArrow = '';

function renderIpStats(stats) {
    var body = Utils.el('ip-stats-body');
    var countEl = Utils.el('ip-stats-count');
    if (!body) return;
    if (!stats || stats.length === 0) {
        body.innerHTML = '<tr><td colspan="5" style="text-align:center;color:var(--text3)">No data</td></tr>';
        if (countEl) countEl.textContent = '-';
        return;
    }
    countEl.textContent = stats.length + ' IPs';
    var html = '';
    for (var i = 0; i < stats.length; i++) {
        var s = stats[i];
        html += '<tr>';
        html += '<td class="ip-addr">' + escHtml(s.ip || '--') + '</td>';
        html += '<td>' + (s.count || 0) + '</td>';
        html += '<td class="token-num token-prompt">' + formatTokenNum(s.prompt_tokens) + '</td>';
        html += '<td class="token-num token-eval">' + formatTokenNum(s.eval_tokens) + '</td>';
        html += '<td class="token-num token-total">' + formatTokenNum(s.total_tokens) + '</td>';
        html += '</tr>';
    }
    body.innerHTML = html;
}

function renderKvCache(kvc) {
    if (!kvc || typeof kvc !== 'object') return;
    var captured = kvc.captured || false;
    var summary = kvc.summary || {};
    var cards = kvc.cards || [];
    var pct = summary.pct || 0;
    var totalKv = summary.kv_total_mb || 0;
    var usedKv = summary.kv_used_mb || 0;
    var freeKv = summary.kv_free_mb || 0;
    var kvPerToken = summary.kv_per_token_bytes || 0;

    // Trend
    if (_kv_prevPct !== null && captured) {
        if (pct > _kv_prevPct + 2) _kv_trendArrow = ' \u2191';
        else if (pct < _kv_prevPct - 2) _kv_trendArrow = ' \u2193';
        else _kv_trendArrow = '';
    }
    if (captured) _kv_prevPct = pct;

    // KV Total
    var el = Utils.el('inf-kv-total');
    if (el) {
        if (totalKv > 0 || captured) el.textContent = formatMbShort(usedKv) + '/' + formatMbShort(totalKv);
        else el.textContent = '--';
    }
    // Remaining
    el = Utils.el('inf-kv-remaining');
    if (el) el.textContent = (captured && freeKv > 0) ? formatMbShort(freeKv) : '--';
    // Tokens
    el = Utils.el('inf-kv-tokens');
    if (el) {
        if (captured && freeKv > 0 && kvPerToken > 0) {
            var est = Math.round((freeKv * 1024 * 1024) / kvPerToken);
            el.textContent = '~' + formatNumberShort(est);
        } else { el.textContent = '~--'; }
    }
    // Physical free
    el = Utils.el('inf-kv-phys-free');
    if (el) {
        var totalPhysFree = 0;
        for (var ci = 0; ci < cards.length; ci++) { totalPhysFree += (cards[ci].free_mb || 0); }
        el.textContent = (captured || cards.length > 0) ? formatMbShort(totalPhysFree) : '--';
    }
    // Token usage
    el = Utils.el('kv-token-usage');
    if (el) {
        if (captured && summary.kv_total_tokens > 0) {
            el.textContent = formatTokenNum(summary.kv_tokens || 0) + ' / ' + formatTokenNum(summary.kv_total_tokens) + ' (' + (summary.tokens_pct || 0).toFixed(1) + '%)';
        } else { el.textContent = '--'; }
    }
    // Context used
    el = Utils.el('kv-ctx-used');
    if (el) {
        var ctxUsed = summary.ctx_size_used || 0;
        el.textContent = ctxUsed > 0 ? (formatTokenNum(ctxUsed) + ' tokens') : '0 tokens';
    }
    // Source
    el = Utils.el('kv-calc-source');
    if (el) {
        var src = summary.source || '--';
        var conf = summary.confidence || '--';
        el.textContent = src + ' / Conf ' + conf;
        el.style.color = conf === 'high' ? 'var(--green)' : conf === 'medium' ? 'var(--yellow)' : 'var(--red)';
    }
    // Verify delta
    el = Utils.el('kv-verify-delta');
    if (el) {
        var delta = summary.verify_delta_mb || 0;
        el.textContent = delta > 0 ? Math.abs(delta).toFixed(0) + ' MB' : '< 1 MB';
        el.style.color = delta > 768 ? 'var(--red)' : delta > 256 ? 'var(--yellow)' : 'var(--green)';
    }
    // Cards
    for (var ci = 0; ci < Math.min(2, cards.length); ci++) {
        var c = cards[ci];
        var nameEl = Utils.el('kv-card-' + ci + '-name');
        var valEl = Utils.el('kv-card-' + ci + '-val');
        var remainEl = Utils.el('kv-card-' + ci + '-remain');
        var physEl = Utils.el('kv-card-' + ci + '-phys');
        var pctElCard = Utils.el('kv-card-' + ci + '-pct');
        if (nameEl) nameEl.textContent = c.name || ('GPU ' + ci);
        if (valEl) valEl.textContent = (c.kv_used_mb || c.used_mb || 0) > 0 ? (formatMbShort(c.kv_used_mb || c.used_mb) + '/' + formatMbShort(c.kv_total_mb || c.total_mb)) : '--';
        if (remainEl) remainEl.textContent = (c.kv_free_mb || c.free_mb || 0) > 0 ? formatMbShort(c.kv_free_mb || c.free_mb) : '--';
        if (physEl) physEl.textContent = (c.free_mb || 0) > 0 ? formatMbShort(c.free_mb) : '--';
        if (pctElCard) pctElCard.textContent = (c.kv_pct || c.pct || 0).toFixed(0) + '%';
    }
}

function renderLlmMetrics(metrics) {
    if (!metrics) return;
    var setText = function(id, val) {
        var el = Utils.el(id);
        if (el && val !== undefined) el.textContent = val;
    };
    setText('llm-ttft', metrics.ttft_ms ? formatMsShort(metrics.ttft_ms) : '--');
    setText('llm-tpot', metrics.tpot_ms ? formatMsShort(metrics.tpot_ms) : '--');
    setText('llm-prompt-total', metrics.total_prompt_tokens ? formatTokenNum(metrics.total_prompt_tokens) : '--');
    setText('llm-eval-total', metrics.total_eval_tokens ? formatTokenNum(metrics.total_eval_tokens) : '--');
    setText('llm-kv-hit', metrics.kv_hit_rate != null ? (metrics.kv_hit_rate * 100).toFixed(1) + '%' : '--');
    setText('llm-spec-accept', metrics.spec_accept_rate != null ? (metrics.spec_accept_rate * 100).toFixed(1) + '%' : '--');
    setText('llm-spec-draft', metrics.spec_draft_length != null ? metrics.spec_draft_length.toFixed(1) : '--');
    setText('llm-spec-speedup', metrics.spec_speedup != null ? metrics.spec_speedup.toFixed(2) + 'x' : '--');
}

function toggleKvBreakdown() {
    var detail = Utils.el('kv-breakdown-detail');
    var arrow = Utils.el('kv-breakdown-arrow');
    if (!detail) return;
    var show = detail.style.display !== 'block';
    detail.style.display = show ? 'block' : 'none';
    if (arrow) arrow.textContent = show ? '\u25bc' : '\u25b6';
}
