// utils.js - Common Utility Functions
// No DOM caching to avoid innerHTML invalidation bugs

window.Utils = {
    escHtml: function(s) {
        return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
    },
    pctColor: function(p) {
        if (p < 50) return '#3fb950';
        if (p < 70) return '#d29922';
        if (p < 85) return '#db6d28';
        return '#f85149';
    },
    fmtSize: function(b) {
        if (b >= 1073741824) return (b / 1073741824).toFixed(1) + ' GB';
        if (b >= 1048576) return (b / 1048576).toFixed(1) + ' MB';
        return (b / 1024).toFixed(1) + ' KB';
    },
    fmtSpeed: function(bps) {
        if (bps >= 1073741824) return (bps / 1073741824).toFixed(2) + ' GB/s';
        if (bps >= 1048576) return (bps / 1048576).toFixed(2) + ' MB/s';
        if (bps >= 1024) return (bps / 1024).toFixed(1) + ' KB/s';
        return bps.toFixed(0) + ' B/s';
    },
    formatTokenNum: function(n) {
        if (!n || n === 0) return '-';
        if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
        if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
        return n.toString();
    },
    formatMbShort: function(mb) {
        if (!mb && mb !== 0) return '--';
        if (mb >= 1024) return (mb / 1024).toFixed(1) + 'G';
        return Math.round(mb) + 'M';
    },
    formatMsShort: function(ms) {
        if (!ms && ms !== 0) return '--';
        if (ms >= 1000) return (ms / 1000).toFixed(1) + 's';
        return Math.round(ms) + 'ms';
    },
    formatNumberShort: function(n) {
        if (!n && n !== 0) return '--';
        if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
        if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
        return n.toString();
    },
    hexToRgba: function(hex, a) {
        var r = parseInt(hex.slice(1,3), 16);
        var g = parseInt(hex.slice(3,5), 16);
        var b = parseInt(hex.slice(5,7), 16);
        return 'rgba(' + r + ',' + g + ',' + b + ',' + a + ')';
    },
    el: function(id) {
        return document.getElementById(id);
    }
};

// Global aliases
var escHtml = Utils.escHtml;
var pctColor = Utils.pctColor;
var fmtSize = Utils.fmtSize;
var fmtSpeed = Utils.fmtSpeed;
var formatTokenNum = Utils.formatTokenNum;
var formatMbShort = Utils.formatMbShort;
var formatMsShort = Utils.formatMsShort;
var formatNumberShort = Utils.formatNumberShort;
var hexToRgba = Utils.hexToRgba;
