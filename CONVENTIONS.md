# control-api 编码规约

约束人写的代码与 AI 写的代码。目标：**文件、方法、依赖不随时间暴涨**。
本文件为 L3 权柄文档（18 章）：AI 顺行编码时必须遵守，修订只能由人进行。

## 1. 规模红线（硬约束）

| 项 | 上限 | 超限处理 |
|---|------|---------|
| 单文件 | 300 行 | 拆分为**同包**多文件（`store.go` → `store.go` + `store_task.go`），不新建包 |
| 单函数 | 60 行 | 提取子函数 |
| 单包文件数 | 8 个 | 说明该包承担了多个领域，拆新包（需人评审） |
| 依赖（go.mod require） | 12 个 | 加依赖先登记 `DEPENDENCIES.md` 并说明理由 |

## 2. 包与文件组织

```
cmd/control-api/main.go     # 只做：解析子命令 → 调 internal，不写逻辑
internal/<domain>/          # 一个领域一个包，包名=领域名（小写单词）
  <domain>.go               # 主文件：导出类型与入口
  <domain>_<thing>.go       # 拆分文件：按类型/资源
```

- **禁止**：`util/`、`common/`、`helper/` 这类垃圾桶包——共享代码要么属于某领域，要么进 `internal/config`
- main 包不写业务逻辑；internal 包不 import cmd
- 新包创建需在 MR 描述中说明它拥有的领域

## 3. 代码风格

- `gofmt` + `go vet` 全绿才允许提交（ci 检查）
- 错误处理：`fmt.Errorf("...: %w", err)` 逐层包装；**只在 main 里允许退出**；internal 包禁止 `panic`/`os.Exit`/`log.Fatal`
- 日志：stdlib `log`，统一前缀 `[<domain>]`；不打密钥、不打 token
- 禁止全局可变状态；配置只经 `config.Config` 传递（显式参数，不读 env——env 读取只发生在 `config.Load`）
- HTTP：路由注册集中在 `internal/api/routes.go`，handler 一行调服务层

## 4. 测试

- 表驱动测试，文件 `<name>_test.go` 与被测文件同包
- 每个导出函数至少一个用例；状态机迁移必须覆盖非法流转
- 不 mock 数据库：SQLite 用 `:memory:` 实测（零依赖优势要用足）
- **测试先行范围**（2026-08-11 增补）：`engine`/`store`/`kb`/`pipeline` 等领域包的逻辑变更，先写表驱动测试（可先失败）再实现，审批以测试用例为评审对象；`api`/`cmd` 等薄层允许测试与实现同 commit 补齐。理由：对 AI 编码，测试是无法曲解的可执行规约，防"理解错"甚于防"忘了测"

## 5. 依赖与工具

- 新增依赖 → 先更新 `DEPENDENCIES.md`，MR 里说明"为什么标准库/现有依赖不够"
- 优先 stdlib；优先与 control-piekbs 已用依赖对齐（sqlite/mcp/fsnotify/yaml）
- 禁止引入：web 框架大件（gin/echo 之类在骨架期用 stdlib mux）、ORM、反射库

## 6. AI 编码特别条款（18 章落地）

- AI 提交必须引用权柄文档：MR 描述列出依据（docs ID + 段落），引用须经校验真实存在
- AI 不得修改本文件及任何 L1/L2 文档（逆行禁止）；发现规约与需求冲突 → 出报告并暂停
- AI 不写无对应任务的代码：每个 commit 关联 TASK-id 或明确的技术债条目
- 自检清单（review skill 执行）：规模红线 / 错误处理 / 无全局状态 / 无新依赖
