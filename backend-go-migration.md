# 8081 后端迁移到 Go

目标：将 `/data/dashboard/app.py` 的职责逐步收敛到 `/data/inference-hub-v3`，保持 `schema_version=2.0` 契约不变，迁移期间可随时回退到 `dashboard.service`。

## 阶段

1. **契约旁路**：Go 增加 `/api/v2/snapshot` 与 `/api/v2/events`，读取 Go collector 缓存。
2. **双读校验**：完成 system/GPU/inference/KV/deployment/requests/services 字段对齐，并修正日志解析中的 prompt/eval 吞吐边界。
3. **只读切流（已完成）**：Go 通过兼容监听器接管 8081，保留 9092；Python dashboard 已停止并禁用开机自启。
4. **控制面迁移（核心接口已完成）**：模型列表、引擎切换、GPU power limit、KV baseline、quick-switch 复用统一鉴权。
5. **遗留路由收敛（已完成第一轮）**：静态资源、model-manager `/mm/api`、现有 cluster/benchmark 通配代理已覆盖；后续只需按实际使用继续清理兼容别名。

## 回滚原则

- 阶段 1/2 不切生产流量。
- 阶段 3 通过 Go 同进程兼容监听器切换入口；保留 `dashboard.service` 配置和 phase5 二进制备份。
- 任一核心指标出现字段缺失、采集延迟或控制请求异常，立即切回 Python，不回滚 collector 数据。

## 当前状态

- Go V2 契约已在 9092 和 8081 生效。
- 8081 当前由 Go 提供，9092 保留用于回归和上游兼容。
- `dashboard.service` 已停止并禁用自启；回滚时恢复该服务、移除 `DASHBOARD_EXTRA_PORT=8081` 即可。
