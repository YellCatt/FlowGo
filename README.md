# FlowGo

轻量工作流编排引擎：**Web 页面配置 → 调度 → 执行 → 日志** 完整闭环，单个二进制即可运行。

## 功能特性

- **Web 控制台**：Vue 单页内嵌进二进制，无需独立前端工程，离线可用
- **Webhook 触发**：外部系统 POST 一个 URL 即可启动流程
- **Cron 定时调度**：支持 5 段 / 6 段表达式与 `@every 30s` 等描述符，改完即生效无需重启
- **内置节点**：`http`、`shell`、`delay`
- **模板变量**：节点间通过 `{{ .nodes.n1.body }}` 传递数据
- **执行日志**：每次运行记录每个节点的输入、输出、耗时与错误
- **纯 Go 实现**：SQLite 使用 `modernc.org/sqlite` 驱动，`CGO_ENABLED=0` 可编译，单文件分发
- **跨平台**：Linux / Windows / macOS / OpenWrt(mipsle) 均可交叉编译

## 快速开始

### 构建

```bash
# 当前平台
CGO_ENABLED=0 go build -o flowgo

# 交叉编译示例
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o flowgo-linux-amd64
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o flowgo.exe
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o flowgo-macos-arm64
```

### 运行

```bash
./flowgo
```

浏览器打开 http://localhost:8084 即可进入控制台。首次运行会自动生成 `config/config.yaml` 与 SQLite 数据库。

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

约定：

- `http` 节点响应状态码 ≥ 400 视为失败
- `shell` 节点退出码非 0 视为失败；Windows 默认使用 PowerShell，Linux / macOS 默认 `/bin/sh -c`

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
```

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| server.port | 服务端口 | 8084 |
| database.path | SQLite 数据库路径 | ./data.db |
| log.path | 日志目录 | ./logs |
| log.level | 日志级别（debug/info/warn/error） | info |

## 项目结构

```
flowgo/
├── config/          # 配置加载与数据库初始化
├── controller/      # HTTP 请求处理器
├── engine/          # 执行引擎：拓扑排序、模板渲染、日志落库
├── logger/          # Zap 日志组件
├── model/           # 数据模型与图结构
├── node/            # 内置节点执行器（http / shell / delay）
├── repository/      # 数据访问层
├── router/          # 路由注册
├── scheduler/       # Cron 定时调度
├── service/         # 业务逻辑层
├── web/             # 内嵌的 Vue 单页控制台
└── main.go          # 入口文件
```

扩展新节点类型只需实现 `node.Executor` 接口并调用 `node.Register`，
随后在 `main.go` 中导入该包即可，无需改动引擎与前端。

## 技术栈

- **语言**: Go 1.22+
- **数据库**: SQLite（modernc.org/sqlite，纯 Go 无 CGO）
- **ORM**: GORM（glebarez/sqlite 驱动）
- **调度**: robfig/cron
- **日志**: Zap
- **前端**: Vue 3（运行时内嵌，无构建步骤）
- **系统监控**: gopsutil

## 许可证

MIT License
