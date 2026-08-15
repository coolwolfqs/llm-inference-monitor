/**
 * Deploy Prefs - 按模型持久化部署参数的公共模块
 * 适用于：仪表盘、模型管理器 等多个页面
 *
 * 用法:
 *   var prefs = new DeployPrefs('my_storage_key', { ctxIdx: 3, ngl: '99', ... });
 *   var current = prefs.get('modelName');     // 获取某模型的参数
 *   prefs.save('modelName', current);          // 保存某模型的参数
 *   prefs.get('otherModel');                   // 获取另一个模型的参数（独立记忆）
 */

function DeployPrefs(storageKey, defaults) {
    this.storageKey = storageKey || 'deploy_prefs';
    this.defaults = defaults || {};
    this._all = {};
    this._load();
}

// 从 localStorage 加载所有模型的参数
DeployPrefs.prototype._load = function() {
    try {
        var raw = localStorage.getItem(this.storageKey);
        if (raw) this._all = JSON.parse(raw);
    } catch(e) {
        this._all = {};
    }
};

// 保存所有数据到 localStorage
DeployPrefs.prototype._persist = function() {
    try {
        localStorage.setItem(this.storageKey, JSON.stringify(this._all));
    } catch(e) {}
};

// 获取指定模型的参数（没有则返回默认值副本）
DeployPrefs.prototype.get = function(modelName) {
    var base = {};
    var keys = Object.keys(this.defaults);
    for (var i = 0; i < keys.length; i++) {
        base[keys[i]] = this.defaults[keys[i]];
    }
    var saved = this._all[modelName];
    if (saved) {
        for (var j = 0; j < keys.length; j++) {
            var k = keys[j];
            if (saved.hasOwnProperty(k)) base[k] = saved[k];
        }
    }
    return base;
};

// 保存指定模型的参数
DeployPrefs.prototype.save = function(modelName, prefs) {
    if (!modelName || !prefs) return;
    this._all[modelName] = {};
    var keys = Object.keys(this.defaults);
    for (var i = 0; i < keys.length; i++) {
        var k = keys[i];
        if (prefs.hasOwnProperty(k)) this._all[modelName][k] = prefs[k];
    }
    this._persist();
};

// 删除指定模型的参数
DeployPrefs.prototype.remove = function(modelName) {
    if (this._all[modelName]) {
        delete this._all[modelName];
        this._persist();
    }
};

// 清空所有参数
DeployPrefs.prototype.clearAll = function() {
    this._all = {};
    this._persist();
};

// 获取所有已保存的模型名称列表
DeployPrefs.prototype.listModels = function() {
    return Object.keys(this._all);
};
