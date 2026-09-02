# FlowGo

[![Go Version](https://img.shields.io/badge/Go-1.22%2B-blue)](https://go.dev)
[![Build Status](https://github.com/example/flowgo/actions/workflows/build.yml/badge.svg)](https://github.com/example/flowgo/actions/workflows/build.yml)
[![License](https://img.shields.io/badge/License-MIT-green)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20OpenWrt-lightgrey)](#交叉编译示例)

轻量工作流编排引擎：**Web 页面配置 → 调度 → 执行 → 日志** 完整闭环，单个二进制即可运行。

## 工作原理

```
┌─────────────┐     ┌─────────────┐     ┌─────────────────┐     ┌──────────────┐
│   Cron 调度  │     │  Webhook    │     │  手动触发        │     │  API 调用     │
└──────┬──────┘     └──────┬──────┘     └────────┬────────┘     └──────┬───────┘
       │                    │                     │                      │
       └───────────┬────────┴───────────┬────────┘                      │
                   ▼                    ▼                               ▼
            ┌────────────────────────────────────────────────┐
            │              执行引擎 (Engine)                 │
            │  ┌─────────────────────────────────────────┐   │
            │  │  拓扑排序 → 模板渲染 → 节点执行 → 日志    │   │
            │  └─────────────────────────────────────────┘   │
            └────────────────────────────────────────────────┘
                             │
             ┌───────────────┼───────────────┐
             ▼               ▼               ▼
        ┌──────────┐   ┌──────────┐   ┌──────────────┐
        │   HTTP   │   │  Shell   │   │  ai_agent    │
        │  节点    │   │  节点    │   │  节点 (LLM)  │
        └──────────┘   └──────────┘   └──────────────┘
```

## 功能特性

- **Web 控制台**：Vue 单页内嵌进二进制，无需独立前端工程，离线可用
- **Webhook 触发**：外部系统 POST 一个 URL 即可启动流程
- **Cron 定时调度**：支持 5 段 / 6 段表达式与 `@every 30s` 等描述符，改完即生效无需重启
- **内置节点**：`http`、`shell`、`delay`
- **AI 增强节点 `ai_agent`**：DAG 骨架由人编排，AI 只在节点内部做分析并按规则调用系统工具
- **模板变量**：节点间通过 `{{ .nodes.n1.body }}` 传递数据
- **执行日志**：每次运行记录每个节点的输入、输出、耗时与错误
- **纯 Go 实现**：SQLite 使用 `modernc.org/sqlite` 驱动，`CGO_ENABLED=0` 可编译，单文件分发
- **跨平台**：Linux / Windows / macOS / OpenWrt(mipsle) 均可交叉编译
- **系统状态监控**：内置 CPU、内存、磁盘 IO、网络实时监控接口
- **优雅退出**：收到 SIGINT/SIGTERM 信号后等待在途请求完成再退出

## 快速开始

### 构建

```bash
# 当前平台
CGO_ENABLED=0 go build -o flowgo

# 交叉编译示例
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o flowgo-linux-amd64
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o flowgo.exe
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o flowgo-macos-arm64
CGO_ENABLED=0 GOOS=linux   GOARCH=mipsle GOMIPS=softfloat go build -o flowgo-openwrt-mipsle
```

### 运行

```bash
./flowgo
```

浏览器打开 http://localhost:8084 即可进入控制台。首次运行会自动生成 `config/config.yaml` 与 SQLite 数据库。

### 一键构建与发布

推送到 `main` 分支时，GitHub Actions 会自动构建 6 个平台的二进制文件，并上传至 **Dev Release**（固定 tag: `dev-latest`）：

| 目标平台 | 架构 | 文件名 |
|---------|------|--------|
| Linux | amd64 / arm64 / mipsle | `flowgo_linux_*.tar.gz` |
| macOS | amd64 / arm64 | `flowgo_darwin_*.tar.gz` |
| Windows | amd64 | `flowgo_windows_amd64.tar.gz` |

## 使用说明

### 1. 编排流程

在控制台新建工作流，添加节点并为其配置参数。节点执行顺序由「依赖的上游节点」决定：

- 配置了依赖 → 按依赖关系拓扑排序执行（自动检测环路）
- 未配置依赖 → 按节点在列表中的顺序执行

任一节点失败即中断整条流程，后续节点不再执行。

### 2. 内置节点

| 类型 | 说明 | 配置参数 | 输出变量 |
|------|------|----------|----------|
| `http` | 发起 HTTP 请求 | `method`(默认 GET)、`url`、`headers`(对象)、`body`、`timeout`(秒，默认 30) | `status_code`、`body`、`headers`、`duration_ms` |
| `shell` | 执行系统命令 | `command`、`workdir`(可选)、`timeout`(秒，默认 60) | `exit_code`、`stdout`、`stderr`、`duration_ms` |
| `delay` | 等待指定时长 | `seconds` | `seconds`、`slept_ms`、`resume_at` |
| `ai_agent` | 大模型分析 + 调用内置工具 | 见 [AI 增强工作流](#6-ai-增强工作流ai_agent-节点) | `answer`、`tool_calls`、`iterations`、`usage`、`duration_ms` |

约定：

- `http` 节点响应状态码 ≥ 400 视为失败
- `shell` 节点退出码非 0 视为失败；Windows 默认使用 PowerShell，Linux / macOS 默认 `/bin/sh -c`
- `ai_agent` 节点达到最大轮次不会失败，而是降级输出最后一轮的结论（`max_iterations_reached = true`）

### 3. 模板变量

节点配置中的字符串支持 Go template 语法，可用变量：

| 变量 | 说明 | 示例 |
|------|------|------|
| `.trigger` | 触发时传入的负载（Webhook 请求体 / 表单 / 查询参数） | `{{ .trigger.order_id }}` |
| `.nodes.<节点ID>` | 已执行节点的输出 | `{{ .nodes.n1.status_code }}` |
| `.workflow` | 工作流信息 | `{{ .workflow.name }}` |
| `.run` | 本次运行信息 | `{{ .run.id }}` |

> 前导点可省略，写成 `{{ trigger.order_id }}` 效果相同。
> 节点 ID 若含特殊字符（如 `node-1`），请使用 `{{ index .nodes "node-1" "body" }}`。

示例：`shell` 节点的命令写 `echo user={{ .trigger.user }} code={{ .nodes.n1.status_code }}`，
即可引用 Webhook 传入的参数和上游 HTTP 节点的响应状态码。

### 4. Webhook 触发

每个工作流自动生成唯一的 Webhook 密钥，调用地址形如：

```
http://localhost:8084/hook/<webhook_key>
```

```bash
curl -X POST http://localhost:8084/hook/<webhook_key> \
  -H "Content-Type: application/json" \
  -d '{"order_id": 1024, "user": "alice"}'
```

响应示例：

```json
{"run_id": 12, "status": "success", "detail": "/api/runs/12"}
```

请求体（JSON）、表单字段与查询参数都会解析进 `.trigger` 变量。工作流停用后 Webhook 返回错误。

### 5. Cron 定时调度

在「Cron 表达式」中填写即可，支持：

- 5 段：`分 时 日 月 周`，如 `0 8 * * *`（每天 8:00）
- 6 段：首位为秒，如 `*/30 * * * * *`（每 30 秒）
- 描述符：`@every 30s`、`@hourly`、`@daily`、`@weekly`

保存后调度器自动重载，无需重启服务。`/health` 接口的 `scheduler_jobs` 可查看当前生效的定时任务数。

### 6. AI 增强工作流（`ai_agent` 节点）

**模式：AI 增强的固定工作流**——DAG 骨架由人预先编排，AI 只活在 `ai_agent` 这一个节点内部。

| 模式 | 特点 |
|------|------|
| 全静态编排 | 所有参数写死，AI 完全不参与 |
| 完全自由 Agent | 没有固定工作流，AI 自己生成整套步骤，流程不可控 |
| **AI 增强工作流（本系统）** | **DAG 拓扑由人配置不变**，节点内部由 AI 分析数据并调用系统已注册的工具，执行顺序仍由连线决定 |

执行流程：

1. 引擎按拓扑排序执行到 `ai_agent` 节点，把**直接上游节点的输出**与触发数据作为上下文交给大模型
2. 大模型分析后输出工具调用指令（只能调用系统已注册的 `http-call` / `shell-run` / `delay-sleep`）：

   ```json
   {"tool_name":"shell-run","args":{"command":"echo 检测到目标数据"}}
   ```

3. Go 层拦截解析 → 执行对应工具 → 把结果回传给大模型继续分析（可多轮）
4. 大模型给出最终结论，作为本节点输出，DAG 继续跑下一个节点

边界保护：

- AI **不能新增、删除或跳过节点**，活动范围锁死在节点内部
- 只能调用节点配置里授权的工具（默认全部内置工具）
- 最大思考轮次默认 5 轮（上限 20），防止死循环；超时时间与工具参数上限均有限制
- `api_key` 写入执行日志前自动脱敏

配置示例（n1 http → n2 ai_agent → n3 delay）：

```json
{
  "id": "n2",
  "type": "ai_agent",
  "config": {
    "system_prompt": "分析上游 http 返回的数据，命中标记时调用 shell-run 输出日志，最后给出结论",
    "model": "qwen-plus",
    "base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1",
    "api_key": "sk-xxx",
    "max_iterations": 5,
    "tools": ["shell-run", "http-call"]
  }
}
```

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `system_prompt` | 任务指令，支持模板变量 | 通用分析指令 |
| `user_prompt` | 附加指令，拼在输入数据之后 | 空 |
| `model` / `base_url` / `api_key` | 模型与接口（OpenAI 兼容），留空取 `config.yaml` 的 `llm.*` | `gpt-4o-mini` / `https://api.openai.com/v1` |
| `timeout` | 单次大模型请求超时（秒） | 60 |
| `max_iterations` | 思考 + 工具调用的最大轮次（上限 20） | 5 |
| `temperature` | 采样温度 | 0.2 |
| `max_tokens` | 单次回复最大 token，0 表示不限制 | 0 |
| `tools` | 允许调用的工具名单，留空表示全部 | 全部 |
| `native_tools` | 是否使用原生 function calling；关闭则走 JSON 文本协议 | false |
| `max_tool_output` | 工具结果回传给大模型的最大字符数 | 4000 |

下游引用：`{{ .nodes.n2.answer }}` 拿到 AI 的结论，`{{ .nodes.n2.tool_calls }}` 可查看它调用过哪些工具。

## API 接口

| 方法 | 路径 | 描述 |
|------|------|------|
| GET | `/` | Web 控制台 |
| GET | `/health` | 健康检查（含定时任务数） |
| GET | `/status` | 系统状态监控（CPU / 内存 / 网速 / 磁盘 IO） |
| GET | `/api/workflows` | 工作流列表 |
| POST | `/api/workflows` | 创建工作流 |
| GET | `/api/workflows/{id}` | 工作流详情 |
| PUT | `/api/workflows/{id}` | 更新工作流 |
| DELETE | `/api/workflows/{id}` | 删除工作流 |
| POST | `/api/workflows/{id}/run` | 手动触发一次运行 |
| GET | `/api/node-types` | 内置节点类型 |
| GET | `/api/agent-tools` | `ai_agent` 节点内部可调用的工具清单 |
| GET | `/api/runs` | 运行记录列表（支持 `workflow_id`、`limit`） |
| GET | `/api/runs/{id}` | 运行详情（含全部节点日志） |
| DELETE | `/api/runs/{id}` | 删除运行记录 |
| POST/GET | `/hook/{key}` | Webhook 触发 |

## 配置文件

`config/config.yaml` 首次运行时自动生成：

```yaml
server:
  port: 8084

database:
  path: ./data.db

log:
  path: ./logs
  level: info

# ai_agent 节点的默认值，节点配置中单独填写的字段优先级更高
llm:
  base_url: https://api.openai.com/v1
  api_key: ""
  model: gpt-4o-mini
  timeout: 60
```

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| server.port | 服务端口 | 8084 |
| database.path | SQLite 数据库路径 | ./data.db |
| log.path | 日志目录 | ./logs |
| log.level | 日志级别（debug/info/warn/error） | info |
| llm.base_url | 大模型接口地址（OpenAI 兼容） | https://api.openai.com/v1 |
| llm.api_key | 大模型密钥，`ai_agent` 节点未单独配置时使用 | 空 |
| llm.model | 默认模型名 | gpt-4o-mini |
| llm.timeout | 大模型请求超时（秒） | 60 |

## 目录结构

```
FlowGo/
├── .github/workflows/build.yml   # CI: 自动构建并发布到 Dev Release
├── config/                       # 配置加载与数据库初始化
│   ├── config.go
│   ├── config.yaml               # 运行时配置（首次启动自动生成）
│   └── database.go
├── controller/                   # HTTP 请求处理器
│   ├── workflow_controller.go
│   ├── run_controller.go
│   ├── webhook_controller.go
│   └── status_controller.go
├── engine/                       # 执行引擎：拓扑排序、模板渲染、日志落库
│   ├── engine.go
│   └── template.go
├── node/                         # 内置节点执行器与工具注册表
│   ├── node.go                   # 节点接口定义与注册中心
│   ├── http_node.go
│   ├── shell_node.go
│   ├── delay_node.go
│   ├── ai_agent_node.go
│   ├── agent_loop.go             # AI 多轮对话循环
│   ├── llm_client.go             # OpenAI 兼容 API 客户端
│   └── tool_registry.go          # ai_agent 内部可调用工具注册表
├── repository/                   # 数据访问层（GORM + SQLite）
├── service/                      # 业务逻辑层
├── scheduler/                    # Cron 定时调度（robfig/cron/v3）
├── router/                       # 路由注册（Go 1.22 ServeMux）
├── logger/                       # Zap 日志组件
├── model/                        # 数据模型
├── web/                          # 内嵌的 Vue 3 单页控制台
│   ├── index.html
│   ├── assets/vue.global.prod.js
│   └── web.go                    # 嵌入资源入口
├── main.go                       # 程序入口
└── go.mod
```

扩展新节点类型只需实现 `node.Executor` 接口并调用 `node.Register`，
随后在 `main.go` 中导入该包即可，无需改动引擎与前端。

## 技术栈

| 类别 | 技术 | 说明 |
|------|------|------|
| 语言 | Go 1.22+ | 利用新泛型与 net/http 新路由语法 |
| 数据库 | SQLite (modernc.org/sqlite) | 纯 Go 实现，零 CGO |
| ORM | GORM + glebarez/sqlite 驱动 | SQLite 专用驱动 |
| 调度 | robfig/cron/v3 | 支持 5/6 段 cron 与 `@every` 描述符 |
| 日志 | Uber Zap | 高性能结构化日志 |
| 系统监控 | shirou/gopsutil/v3 | CPU / 内存 / 网络 / 磁盘 IO |
| HTTP 框架 | 标准库 net/http | Go 1.22 ServeMux，零第三方依赖 |
| 前端 | Vue 3 (runtime global build) | 无构建步骤，JS 直接嵌入二进制 |
| AI 客户端 | 自研 HTTP 客户端 | OpenAI 兼容协议，支持 function calling |

## 许可证

MIT License