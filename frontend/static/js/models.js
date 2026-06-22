// models.js - Model Parsing, Info Panel, Engine Switching
var MODEL_PROFILES = {
    "Generic-LLM": { arch: "Dense", params: "7B-70B", ref_tps: {8:40,32:35,64:30,128:25} },
};

var QUANT_LABELS = {
    "IQ3_XXS": "Ultra-light (3.0bpw)", "IQ4_XS": "Light (4.0bpw)", "IQ4_NL": "Low-loss (4.3bpw)",
    "Q4_K_M": "Balanced (4.5bpw)", "Q5_K_M": "High-quality (5.2bpw)", "Q4_K_S": "Compact (4.2bpw)",
    "MXFP4": "MXFP4 Format", "BF16": "BF16 Precision", "Q6_K": "High Quality (6.0bpw)",
};

function shortModelName(filename) {
    if (!filename) return '';
    var basename = filename.split('/').pop() || filename;
    return basename.replace(/\\.gguf$/, '').replace(/-(IQ[0-9_]+[A-Z]*|Q[0-9]_[A-Z]+[A-Z_]*|MXFP4)/, '');
}

function parseModelFilename(filename) {
    if (!filename) return null;
    var basename = filename.split('/').pop() || filename;
    var base = basename.replace('.gguf', '');
    var family = null;
    if (base.startsWith('Qwen')) family = 'Generic-LLM';
    else if (base.startsWith('gemma')) family = 'Generic-LLM';
    else if (base.startsWith('Llama')) family = 'Generic-LLM';
    var profile = family ? MODEL_PROFILES[family] || MODEL_PROFILES['Generic-LLM'] : null;
    // Extract quant
    var quant = null;
    var quants = ['IQ3_XXS','IQ4_XS','IQ4_NL','Q4_K_M','Q5_K_M','Q4_K_S','MXFP4','BF16','Q6_K'];
    for (var i = 0; i < quants.length; i++) {
        if (base.indexOf(quants[i]) >= 0) { quant = quants[i]; break; }
    }
    return { family: family, base: base, arch: profile ? profile.arch : '', params: profile ? profile.params : '', quant: quant };
}

function renderModelInfo(filename, config) {
    var content = Utils.el('model-info-content');
    if (!content) return;
    var info = parseModelFilename(filename);
    if (!info) { content.innerHTML = '<div class="empty-msg">Cannot parse</div>'; return; }
    var html = '';
    html += '<div class="mi-file" title="' + escHtml(filename) + '">' + escHtml(shortModelName(filename)) + '</div>';
    html += '<div class="mi-section"><div class="mi-label">Model Profile</div><div class="mi-tags">';
    html += '<span class="mi-tag arch">' + (info.arch || '--') + '</span>';
    html += '<span class="mi-tag param">' + (info.params || '--') + '</span>';
    html += '<span class="mi-tag quant">' + (QUANT_LABELS[info.quant] || info.quant || '-') + '</span>';
    html += '</div></div>';
    if (config && Object.keys(config).length > 0) {
        html += '<div class="mi-section"><div class="mi-label">Config</div><div class="mi-parse">';
        var shown = 0;
        for (var k in config) {
            if (shown >= 6) break;
            if (typeof config[k] === 'string' || typeof config[k] === 'number') {
                html += '<span class="seg">' + k + '</span><span class="sep">=</span><span class="meaning">' + config[k] + '</span>';
                if (shown < 5) html += ' &middot; ';
                shown++;
            }
        }
        html += '</div></div>';
    }
    content.innerHTML = html;
}
