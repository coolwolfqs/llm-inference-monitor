// deploy-prefs.js - Per-Model Deployment Preferences (localStorage)

function DeployPrefs(storageKey, defaults) {
    this.storageKey = storageKey || 'deploy_prefs';
    this.defaults = defaults || {};
    this._all = {};
    this._load();
}

DeployPrefs.prototype._load = function() {
    try { var raw = localStorage.getItem(this.storageKey); if (raw) this._all = JSON.parse(raw); }
    catch(e) { this._all = {}; }
};

DeployPrefs.prototype._persist = function() {
    try { localStorage.setItem(this.storageKey, JSON.stringify(this._all)); } catch(e) {}
};

DeployPrefs.prototype.get = function(modelName) {
    var base = {};
    var keys = Object.keys(this.defaults);
    for (var i = 0; i < keys.length; i++) base[keys[i]] = this.defaults[keys[i]];
    var saved = this._all[modelName];
    if (saved) { for (var j = 0; j < keys.length; j++) { var k = keys[j]; if (saved.hasOwnProperty(k)) base[k] = saved[k]; } }
    return base;
};

DeployPrefs.prototype.save = function(modelName, prefs) {
    if (!modelName || !prefs) return;
    this._all[modelName] = {};
    var keys = Object.keys(this.defaults);
    for (var i = 0; i < keys.length; i++) { var k = keys[i]; if (prefs.hasOwnProperty(k)) this._all[modelName][k] = prefs[k]; }
    this._persist();
};

DeployPrefs.prototype.remove = function(modelName) { if (this._all[modelName]) { delete this._all[modelName]; this._persist(); } };
DeployPrefs.prototype.clearAll = function() { this._all = {}; this._persist(); };
DeployPrefs.prototype.listModels = function() { return Object.keys(this._all); };
