// shared/ui-components.js - Reusable UI Components
// Shared between Dashboard and Model Manager

window.SharedUI = {
    // Toast notification
    toast: function(msg, type, duration) {
        type = type || 'info';
        duration = duration || 3000;
        var t = document.getElementById('shared-toast');
        if (!t) {
            t = document.createElement('div');
            t.id = 'shared-toast';
            t.style.cssText = 'position:fixed;top:16px;right:16px;padding:10px 20px;border-radius:6px;color:#fff;font-size:13px;z-index:10000;transform:translateX(120%);transition:transform 0.3s;max-width:320px;';
            document.body.appendChild(t);
        }
        t.textContent = msg;
        t.style.background = type === 'success' ? '#15803d' : type === 'error' ? '#dc2626' : '#2563eb';
        t.style.transform = 'translateX(0)';
        setTimeout(function() { t.style.transform = 'translateX(120%)'; }, duration);
    },

    // Confirm dialog
    confirm: function(msg, callback) {
        if (window._sharedConfirm) window._sharedConfirm.remove();
        var d = document.createElement('div');
        d.id = 'shared-confirm-overlay';
        d.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.6);z-index:9999;display:flex;align-items:center;justify-content:center;';
        d.innerHTML = '<div style="background:var(--bg2,#161b22);border:1px solid var(--border,#30363d);border-radius:12px;padding:24px;max-width:400px;width:90%;">' +
            '<p style="margin-bottom:16px;font-size:14px;">' + msg + '</p>' +
            '<div style="display:flex;gap:8px;justify-content:flex-end;">' +
            '<button id="sc-cancel" style="padding:6px 16px;background:var(--bg3,#0d1117);border:1px solid var(--border,#30363d);border-radius:6px;color:var(--text2,#8b949e);cursor:pointer;">Cancel</button>' +
            '<button id="sc-ok" style="padding:6px 16px;background:#2563eb;border:none;border-radius:6px;color:#fff;cursor:pointer;">Confirm</button>' +
            '</div></div>';
        document.body.appendChild(d);
        window._sharedConfirm = d;
        d.querySelector('#sc-ok').onclick = function() { d.remove(); window._sharedConfirm = null; callback(true); };
        d.querySelector('#sc-cancel').onclick = function() { d.remove(); window._sharedConfirm = null; callback(false); };
    },

    // Format uptime
    formatUptime: function(seconds) {
        var s = parseInt(seconds);
        if (s < 60) return s + 's';
        if (s < 3600) return Math.floor(s / 60) + 'm';
        if (s < 86400) return Math.floor(s / 3600) + 'h ' + Math.floor((s % 3600) / 60) + 'm';
        return Math.floor(s / 86400) + 'd ' + Math.floor((s % 86400) / 3600) + 'h';
    },

    // Format bytes
    formatBytes: function(bytes) {
        var b = parseInt(bytes);
        if (b < 1024) return b + ' B';
        if (b < 1048576) return (b / 1024).toFixed(1) + ' KB';
        if (b < 1073741824) return (b / 1048576).toFixed(1) + ' MB';
        return (b / 1073741824).toFixed(1) + ' GB';
    }
};
