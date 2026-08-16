# 引擎参数注册

每个引擎目录包含 `VERSION.json` 和 `deployment-parameters.json`。参数文件可以通过 `extends` 引用 `../common/deployment-parameters.json`，然后只覆盖该引擎的能力、加载策略、环境变量和 profile。

新增 llama.cpp 引擎时只需要：

1. 放置二进制并填写 `VERSION.json` 的 `binary_path`、版本、后端和设备信息。
2. 复制一个 `deployment-parameters.json`，声明 `profiles`、`load_strategy` 和必要的引擎专属参数。
3. 用目标二进制的 `--help` 和 `--list-devices` 做能力校验。

参数文件是 UI 的配置来源，二进制探测是支持性校验来源。`managed: true` 的字段仍由后端兼容层负责生成旧版启动参数；新参数默认使用 `flag`，环境变量参数使用 `env`。模型、草稿模型和视觉投影文件仍只能从模型中心解析，不能由引擎配置或前端直接注入绝对路径。
