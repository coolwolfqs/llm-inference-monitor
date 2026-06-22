# LLM Inference Monitor (推理服务监控面板)

一体化实时推理服务监控仪表盘，支持 GPU、CPU、内存、磁盘、网络指标采集和推理性能可视化。

> **安全优先设计：** 所有敏感数据（IP、主机名、管理员密钥、磁盘型号、进程 PID）均通过可配置的脱敏中间件自动处理，公开截图也无需担心信息泄露。

---

## 功能特性

### 系统监控
- **GPU**：利用率 / 显存 / 温度 / 功耗 / 风扇 / 频率 / PCIe 链路状态 / 编码器解码器利用率 / 单卡详情
- **CPU**：每核利用率 / 频率 / 温度 / 缓存信息 / 负载均值 / 进程线程数
- **内存**：使用率 / 已用 / 可用 / 缓存 / 缓冲区 / Swap
- **磁盘**：读写速度 / 活动时间 / 分区使用率 / NVMe 温度
- **网络**：吞吐量（收发）/ 适配器详情 / 链路速率

### 推理监控
- Token 生成速度（TPS）实时显示
- KV Cache 利用率和显存分布
- LLM 性能指标：TTFT、TPOT、KV 命中率、MTP 投机解码统计
- 服务健康校验（进程 / GPU / 指标 / KV 交叉验证）
- 基于 IP 的 Token 用量统计（支持 IP 脱敏）

### 引擎管理
- 多引擎支持（llama.cpp / vllm）
- 引擎切换（自动重启推理服务）
- 版本号和 GitHub 源码链接展示

### 系统控制
- 持久化模式切换（自动 / 手动）
- GPU 功耗限制（40%–100%）
- 重启 / 关机（管理员权限保护）
- 明亮 / 暗黑主题（按北京时间自动切换）

### 数据安全
| 数据类型 | 脱敏方式 | 配置项 |
|---------|---------|-------|
| IP 地址 | 部分脱敏 / 全部脱敏 / 不脱敏 | `IP_REDACT_MODE` |
| GPU UUID / 序列号 | 强制替换为 `REDACTED` | 不可关闭 |
| 进程 PID | 归零 | 不可关闭 |
| 磁盘型号 | 仅保留品牌前缀 | 自动处理 |
| 网卡名称 | 替换为 `eth0` | 自动处理 |
| CPU 型号 | 保留型号名，掩掉序列号 | 自动处理 |
| 管理密钥 | 环境变量配置，HMAC 验证 | `ADMIN_KEY` |

---

## 快速开始

### 环境要求
- Python 3.10+
- NVIDIA GPU + nvidia-smi（采集 GPU 指标）
- Linux 系统（采集完整系统指标）
- llama.cpp 服务端 或 vllm（采集推理指标）

### 安装

```bash
git clone https://github.com/coolwolfqs/llm-inference-monitor.git
cd llm-inference-monitor
./scripts/setup.sh
```

### 配置

```bash
cp config.yaml.example config.yaml
# 编辑 config.yaml 修改为你自己的部署地址
```

关键环境变量：

| 变量 | 默认值 | 说明 |
|------|-------|------|
| `MONITOR_HOST` | `0.0.0.0` | 服务监听地址 |
| `MONITOR_PORT` | `8081` | 服务端口 |
| `ADMIN_KEY` | `changeme` | 管理员操作密钥 |
| `INFERENCE_HOST` | `127.0.0.1` | 推理服务地址 |
| `INFERENCE_PORT` | `8080` | 推理服务端口 |
| `IP_REDACT_MODE` | `partial` | IP 脱敏模式：partial/full/none |

### 启动

```bash
./scripts/start.sh
# 或自定义端口：
MONITOR_PORT=9090 ./scripts/start.sh
```

打开 http://localhost:8081 即可查看面板。

---

## 项目结构

```
llm-inference-monitor/
├── backend/                          # Python FastAPI 后端
│   ├── server.py                     # 主服务 + API + SSE 实时推送
│   ├── config.py                     # 配置管理（纯环境变量，无硬编码）
│   ├── collectors/                   # 采集器
│   │   ├── gpu_collector.py          # GPU 指标采集（nvidia-smi）
│   │   ├── cpu_collector.py          # CPU 指标采集（psutil）
│   │   ├── memory_collector.py       # 内存指标采集
│   │   ├── disk_collector.py         # 磁盘 I/O 采集
│   │   ├── network_collector.py      # 网络吞吐采集
│   │   ├── system_collector.py       # 系统信息采集
│   │   └── inference_collector.py    # 推理服务指标采集
│   └── api/
│       └── middleware.py             # 管理员认证 + 数据脱敏
├── frontend/                         # 前端静态文件
│   ├── index.html                    # 仪表盘主页面
│   └── static/
│       ├── css/                      # 样式文件（6 个）
│       │   ├── base.css              # CSS 变量、重置、主题
│       │   ├── layout.css            # 布局：顶栏、导航、分区
│       │   ├── components.css        # 组件：卡片、按钮、表格
│       │   ├── monitor.css           # 图表、仪表盘、GPU 面板
│       │   ├── models.css            # 模型横幅、标签
│       │   └── optimize.css          # 响应式查询
│       └── js/                       # JavaScript 模块（8 个）
│           ├── utils.js              # 工具函数（格式化、转义）
│           ├── charts.js             # Canvas 贝塞尔曲线图表
│           ├── system.js             # 系统控制：主题、持久化、功耗
│           ├── monitor.js            # 主逻辑：数据拉取、SSE、渲染
│           ├── inference.js          # KV Cache、IP 统计、LLM 指标
│           ├── gpu.js                # GPU 卡片、详情、功耗标签
│           ├── models.js             # 模型文件名解析
│           └── deploy-prefs.js       # 模型部署参数持久化
├── config.yaml.example               # 配置模板
├── requirements.txt                  # Python 依赖
├── scripts/
│   ├── start.sh                      # 启动脚本
│   └── setup.sh                      # 安装脚本
├── README.md                         # 英文文档
├── README.zh-CN.md                   # 中文文档
├── LICENSE                           # MIT 许可
└── CONTRIBUTING.md                   # 贡献指南
```

---

## API 接口

| 方法 | 路径 | 说明 | 需认证 |
|------|------|------|:----:|
| GET | `/api/status` | 全量系统状态快照 | 否 |
| GET | `/api/sse` | SSE 实时数据流 | 否 |
| GET | `/api/engines` | 列出可用推理引擎 | 否 |
| POST | `/api/engine/switch` | 切换推理引擎 | 是 |
| POST | `/api/gpu/power_limit` | 设置 GPU 功耗限制 | 是 |
| GET | `/api/settings/persist` | 获取持久化模式 | 否 |
| POST | `/api/settings/persist` | 设置持久化模式 | 否 |

---

## 从原始版到开源版的主要变更

1. **IP 地址脱敏**：所有内网 IP 通过中间件自动替换
2. **管理员密钥外置**：从代码硬编码改为 `ADMIN_KEY` 环境变量
3. **GPU 敏感信息移除**：UUID、序列号强制遮蔽
4. **进程 PID 归零**：不再暴露真实进程号
5. **磁盘型号泛化**：只保留品牌前缀
6. **后端重构**：从单体文件拆分为模块化采集器架构
7. **SSE 推送优化**：2 秒间隔实时推送，30 秒兜底轮询
8. **增量 DOM 更新**：只更新变化的元素，减少重绘

---

## License

MIT License
