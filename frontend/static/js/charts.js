// charts.js - Canvas Chart Rendering (Bezier curves)
var _chartDebounceTimers = {};
var _lastDrawnData = {};

function _chartDataHasChanged(canvasId, data, maxVal) {
    var key = canvasId;
    if (!data || data.length < 2) return false;
    var len = data.length;
    var fingerprint = len + '_';
    for (var i = Math.max(0, len - 3); i < len; i++) {
        fingerprint += (data[i] || 0).toFixed(2) + '_';
    }
    fingerprint += '_' + (maxVal || 100);
    if (_lastDrawnData[key] === fingerprint) return false;
    _lastDrawnData[key] = fingerprint;
    return true;
}

function _precomputePath(data, n, pad, W, H, maxVal) {
    var mv = maxVal || 100;
    var step = (W - pad * 2) / (n - 1 || 1);
    var points = new Array(n);
    for (var i = 0; i < n; i++) {
        var x = pad + i * step;
        var v = Math.min(Math.max(data[i], 0) / mv, 1);
        points[i] = { x: x, y: pad + (H - pad * 2) * (1 - v) };
    }
    return points;
}

function _drawPath(ctx, points, n, pad, H, color, fillAlpha) {
    fillAlpha = fillAlpha || 0.15;
    var grad = ctx.createLinearGradient(0, 0, 0, H);
    grad.addColorStop(0, hexToRgba(color, fillAlpha));
    grad.addColorStop(1, hexToRgba(color, 0.01));
    ctx.beginPath();
    ctx.moveTo(pad, H - pad);
    for (var i = 0; i < n; i++) {
        var p = points[i];
        if (i === 0) ctx.lineTo(p.x, p.y);
        else {
            var pp = points[i - 1];
            var cpx = (pp.x + p.x) / 2;
            ctx.bezierCurveTo(cpx, pp.y, cpx, p.y, p.x, p.y);
        }
    }
    ctx.lineTo(points[n - 1].x, H - pad);
    ctx.closePath();
    ctx.fillStyle = grad;
    ctx.fill();

    ctx.beginPath();
    for (var i = 0; i < n; i++) {
        var p = points[i];
        if (i === 0) ctx.moveTo(p.x, p.y);
        else {
            var pp = points[i - 1];
            var cpx = (pp.x + p.x) / 2;
            ctx.bezierCurveTo(cpx, pp.y, cpx, p.y, p.x, p.y);
        }
    }
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.5;
    ctx.stroke();

    var last = points[n - 1];
    ctx.beginPath();
    ctx.arc(last.x, last.y, 3, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.fill();
    ctx.beginPath();
    ctx.arc(last.x, last.y, 5, 0, Math.PI * 2);
    ctx.strokeStyle = hexToRgba(color, 0.4);
    ctx.lineWidth = 1;
    ctx.stroke();
}

function _drawGrid(ctx, pad, W, H) {
    ctx.strokeStyle = 'rgba(48,54,61,0.4)';
    ctx.lineWidth = 0.5;
    for (var i = 0; i <= 4; i++) {
        var y = pad + (H - pad * 2) * i / 4;
        ctx.beginPath();
        ctx.moveTo(pad, y);
        ctx.lineTo(W - pad, y);
        ctx.stroke();
    }
}

function _setupCanvas(canvasId) {
    var c = document.getElementById(canvasId);
    if (!c) return null;
    var ctx = c.getContext('2d');
    var dpr = window.devicePixelRatio || 1;
    var W = c.clientWidth, H = c.clientHeight;
    if (W === 0 || H === 0) return null;
    c.width = W * dpr;
    c.height = H * dpr;
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, W, H);
    return { ctx: ctx, W: W, H: H };
}

function drawSimpleChart(canvasId, data, color, maxVal) {
    maxVal = maxVal || 100;
    if (!_chartDataHasChanged(canvasId, data, maxVal)) return;
    var setup = _setupCanvas(canvasId);
    if (!setup || !data || data.length < 2) return;
    var ctx = setup.ctx, W = setup.W, H = setup.H;
    var pad = 8;
    var n = data.length;
    var points = _precomputePath(data, n, pad, W, H, maxVal);
    _drawGrid(ctx, pad, W, H);
    _drawPath(ctx, points, n, pad, H, color, 0.12);
}

// ECG / TPS heartbeat chart
function _ecgDraw(canvasId, tps) {
    var setup = _setupCanvas(canvasId);
    if (!setup) return;
    var ctx = setup.ctx, W = setup.W, H = setup.H;
    var pad = 4;
    var mid = H / 2;
    var amp = Math.min(H / 3, 20);

    ctx.strokeStyle = '#3fb950';
    ctx.lineWidth = 1.2;
    ctx.beginPath();
    ctx.moveTo(pad, mid);

    var segments = 30;
    var sw = (W - pad * 2) / segments;
    for (var i = 0; i <= segments; i++) {
        var x = pad + i * sw;
        var phase = (i / segments) * Math.PI * 2;
        var beat = Math.sin(phase * 3);
        if (phase % (Math.PI * 2) < 0.1) {
            beat = -Math.sin(phase * 12) * 2;
        }
        var y = mid + beat * amp * Math.min(tps / 50, 1);
        if (i === 0) ctx.moveTo(x, y);
        else ctx.lineTo(x, y);
    }
    ctx.stroke();
}

var _lastTps = 0;
function updateEcgTps(tps) {
    if (Math.abs(tps - _lastTps) < 0.5 && tps > 0) return;
    _lastTps = tps;
    var canvas = document.getElementById('ecg-canvas');
    if (canvas) _ecgDraw('ecg-canvas', tps);
}
