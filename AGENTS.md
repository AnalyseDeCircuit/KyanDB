# KyanDB Agent Guidelines

本文件适用于整个 KyanDB 仓库。所有参与开发的 AI 代理与贡献者必须遵守本规范；架构细节与包依赖图见 `docs/ARCHITECTURE.md`。

## 基本原则

- **先查真相再动手**：修改代码前必须先读真实代码路径并定位根因，禁止基于猜测写补丁。
- **最小正确方案**：只做当前目标直接要求的功能与改动，不顺手重构无关代码，不为单次使用造通用框架、工厂层或无意义接口包装。
- **未发布代码不留兼容包袱**：仅已发布版本的公开行为、持久化配置格式与外部协议（数据库线协议、MCP 协议）构成兼容合同；开发中尚未发布的结构与格式直接修改或删除，禁止新增迁移逻辑或遗留兜底。
- **先问再猜**：若存在多种合理解释且会显著改变产品行为、数据安全或架构边界，先说明差异并请求确认。
- **所有代码注释使用英文**：注释只说明原因、不变量、所有权或边界条件，不复述代码字面行为。
- **禁止静默降级与吞错**：禁止用 `_ = err` 吞掉错误（仅对明确无害的 `Close` 或只读清理可在注释说明原因）；禁止通过削弱测试或添加测试专用生产分支换取绿灯。

## 硬性架构红线（由 `arch_test.go` 机械强制）

完整包职责与依赖 DAG 见 `docs/ARCHITECTURE.md`。以下边界不可突破：

1. **纯 Go 零 CGO 红线**：全仓禁止 `import "C"` 与依赖 CGO 的驱动（如 `mattn/go-sqlite3`）。所有改动必须满足 `CGO_ENABLED=0 go build ./...`。
2. **`main.go` 只做启动装配**：`main.go` 只负责初始化 `mygo.App`、装配 `internal/runtime` 与打开根窗口；严禁往 `main.go` 堆业务状态、数据库逻辑或大段界面代码。
3. **UI 与核心逻辑彻底解耦**：
   - 仅 `main.go` 与 `internal/ui/...` 允许导入 `github.com/egoist/mygo/ui`。
   - `internal/domain`、`internal/secret`、`internal/config`、`internal/tunnel`、`internal/engine/...`、`internal/sqlguard`、`internal/runtime`、`internal/mcp` **严禁导入 `mygo/ui`**。
   - `internal/ui/...` **严禁直接导入** `database/sql`、`internal/engine/...`、`internal/tunnel`、`internal/config`、`internal/secret`；所有状态读取与动作必须通过 `internal/runtime` 的窄接口和不可变快照进行。
4. **`internal/domain` 保持零外部依赖**：只允许导入 Go 标准库，定义纯值类型、领域错误与核心枚举，不含 I/O、锁或驱动依赖。
5. **按需建包，禁止空壳占位**：`docs/ARCHITECTURE.md` 中的包按里程碑实际落地时再创建，禁止提前建一堆空目录或单文件包装包。
6. **单向依赖，严禁循环**：严格遵守 `docs/ARCHITECTURE.md` 的分层 DAG；如发现跨层需求，先梳理归属层，禁止通过全局变量或 `init()` 绕过依赖方向。

## `mygo` 原生界面与渲染规范

- **渲染帧绝对禁止阻塞 I/O**：`view(c *ui.Context)` 是高频 GPU 渲染回调，严禁在其中发起数据库查询、SSH 握手、磁盘读写、钥匙串查询或长时间计算。所有异步任务必须交由 `internal/runtime` 在后台 goroutine 执行，完成后更新状态快照并触发界面重绘。
- **优先复用 `mygo` 现有能力**：
  - 优先使用 `mygo/ui` 内置组件（`Table`、`Tree`、`Tabs`、`Split`、`Sidebar`、`Editor`、`PopoverMenu`、`Combobox`、`Toast`、`Tooltip`、`Form`、`Theme`）与 `mygo` 原生服务（`mygo.Secrets`、`mygo/i18n`、`dialog`、`clipboard`、`menu`、`window_state`）。
  - 使用不熟悉的 `mygo/ui` 组件前，先查阅本地 Go module 缓存中的 `mygo/ui` 真实源码，确认签名、焦点、快捷键与 `c.Key(...)` 标识规范后再写，禁止自建平替体系。
- **大结果集默认虚拟化**：表数据、查询结果、Schema 树与日志视图必须按可见窗口虚拟化渲染，禁止为全量结果行构造 DOM/控件树或在渲染期做 `O(N)` 全量字符串拼接。
- **视觉规范以 `.interface-design/system.md` 为准**：新增或修改界面前先读它，颜色、尺寸与状态只取 `internal/ui/theme` 的令牌与组件模式，不在视图里写新的颜色值或另起一套控件外观。布局应尽量自解释，避免堆砌多余的次级说明文案。

## 国际化

- **用户可见文本一律进语言包**：按钮、标签、占位符、提示、空状态、错误文案，以及只给辅助技术读的 `Label` / `Tooltip`，都必须来自 `locales/`，视图中不得出现硬编码的界面文字。
- **不进语言包的内容**：用户数据与数据库标识符（表名、列名、单元格值、主机名）、产品与引擎名（KyanDB、PostgreSQL）、SQL 关键字与字面量（`WHERE`、`NULL`）、快捷键符号（`⌘↵`）。
- **语言包结构**：`locales/en.json` 是源语言，每条消息必须写 `@key` 说明（用途与所在位置）及 `placeholders` 类型；当前支持 `en`、`zh-Hans`。新增或修改键时同步维护所有已支持语言，不靠回退英文掩盖缺项（`locales/catalog_test.go` 会拦截缺键）。
- **消息写法**：使用 ICU MessageFormat。数量用 `plural`，按枚举分支用 `select`，禁止在 Go 里拼接句子片段或按语言写 `if`；同一句话保持为一条完整消息，让译文可以调整语序。键名小驼峰，同一区域共用前缀（`sidebar…`、`ledger…`、`plate…`）。
- **视图取文案的方式**：统一通过 `msgs(c)`（即 `locales.In(c.Locale())`）按窗口语言取消息，不直接调用读进程全局语言的 `locales.Xxx()`。数字、紧凑计数、日期与耗时用 `mygo/i18n` 按 `c.Locale()` 格式化，禁止手写千分位或单位换算。
- **分层边界**：只有 `internal/ui/...` 可以导入 `locales`（`arch_test.go` 强制）。`runtime`、`engine`、`sqlguard`、`mcp` 等返回类型化的领域错误与原始值，由界面层翻译。
- **生成代码与审计**：改动 `locales/*.json` 后运行 `GOTOOLCHAIN=go1.27.4 go run github.com/egoist/mygo/cmd/mygo generate` 重新生成并提交 `locales/catalog_gen.go`，然后运行 `python3 scripts/quality/audit_i18n.py`（零编译开销，检查缺键、重键、源描述、ICU 占位符、生成代码同步与直调全局 `locales.Xxx()`）；必要时加 `--run-mygo-check` 或单独执行 `mygo i18n check`。不要手改生成文件。
- **版本号更新**：统一通过 `python3 scripts/release/bump_version.py <SemVer>` 更新 `mygo.json`（支持 `--dry-run`），不手改散落的版本号。

## 并发、连接与生命周期所有权

- **单一运行时所有者**：所有活跃数据库连接池、SSH 隧道、跳板机父连接、正在执行的查询与 MCP 租约均由 `internal/runtime` 统一持有与调度，**严禁**由单个 UI 标签页或弹窗隐式持有连接所有权。
- **标签页关闭 ≠ 断开共享连接**：关闭查询标签页或数据浏览器视图只取消该视图关联的 `context.Context` 查询，不得断开仍被其他标签页、Schema 浏览器或 MCP 租约复用的共享连接池与 SSH 隧道。
- **跳板机引用计数与逆序释放**：多跳 SSH 隧道（`ProxyJump`）必须在子连接活跃期间保持父连接存活；断开连接时按“取消在途查询 → 关闭数据库池 → 关闭子隧道 → 释放无引用父隧道”的顺序确定性清理，严禁泄漏 goroutine 或套接字。
- **每个异步任务都必须可取消**：任何查询、Schema 拉取、导出、连接探测与隧道拨号必须接受 `context.Context`，禁止启动无退出路径的野生 `go func()`。
- **避免锁内 I/O**：严禁在持有 `sync.Mutex` / `sync.RWMutex` 期间执行网络拨号、SQL 执行、钥匙串访问或回调外部闭包。

## 数据库安全与生产防护红线

- **防护逻辑必须下沉到运行时，禁止只靠 UI 禁用按钮**：
  - `ReadOnly` 模式、环境标签（`dev` / `staging` / `prod`）保护与写操作确认门禁必须在 `internal/runtime` 与 `internal/sqlguard` 统一强制执行，UI 与 `internal/mcp` 共享同一门禁路径。
  - 在引擎支持时，只读模式还应叠加连接/事务级只读保障（如 PostgreSQL `default_transaction_read_only=on`、SQLite `mode=ro`）。
- **行内编辑必须走草稿与 SQL Diff 预览**：单元格编辑、新增行、删除行默认进入本地 `StagedEdit` 草稿，由 `internal/sqlguard` 按方言安全转义标识符与参数并生成可审阅 SQL，经用户明确确认后在事务内提交；严禁绕过草稿直接静默写库。
- **参数化与标识符转义**：禁止用字符串拼接构造 SQL 值字面量；表名、列名、Schema 名必须经过对应引擎方言的严格标识符引用函数处理。
- **MCP 代理最小权限**：
  - 本地 MCP 服务仅绑定 loopback（`127.0.0.1` / `::1`），不得暴露原始数据库凭据、密码、私钥或完整含密 DSN。
  - MCP 访问必须绑定具体连接范围与有时效的租约（TTL Lease），默认只读；任何写操作或 DDL 必须暂停并进入人工审批队列，完整记录本地审计日志。

## 凭据与敏感数据安全

- 密码、SSH 口令、私钥内容、Token、含凭据的连接串（DSN）以及可能包含敏感数据的表单元格内容均按敏感数据处理。
- **敏感值严禁落盘明文**：普通配置、连接列表、查询历史、审计日志、错误文本、调试日志、通知和 AI/MCP 上下文严禁出现原始凭据。
- **统一走系统钥匙串**：持久化凭据只能通过 `internal/secret`（底层复用 `mygo.Secrets`）写入系统钥匙串；`internal/config` 的 JSON 文件只存非敏感元数据与不透明的 secret key 引用。
- **防误打印**：敏感结构体必须实现脱敏的 `String()` / `GoString()` / `MarshalJSON()`，防止 `fmt.Printf("%+v", ...)` 或日志序列化意外泄漏。
- **短生命周期与零化**：临时持有的密码/私钥字节切片应尽快用完并覆写清零，减少跨 goroutine 复制。

## 测试与验证标准

- **只写高价值、可拦截真实回归的测试**：围绕核心契约编写测试——架构边界（`arch_test.go`）、SQL 分类与方言转义（`sqlguard`）、暂存编辑 Diff 生成、连接/隧道取消与生命周期清理、凭据脱敏、以及关键 UI 交互（使用 `mygo/ui.NewTester`）。
- **禁止低质量测试**：
  - 禁止只断言 `err == nil`、非空或长度而忽略真实内容、顺序或副作用。
  - 禁止用被测实现自身计算预期结果；禁止恒真断言或未注入敏感输入却断言输出不含敏感值的伪脱敏测试。
  - 并发与取消测试必须真实触发竞争或取消条件，并用 `-race` 验证。
- **集中改完再统一验证**：
  - 常规验证优先运行受影响包：`CGO_ENABLED=0 go test ./internal/<pkg>/...`
  - 修改并发、连接池、隧道、取消或共享状态时，收尾必须运行带竞争检测的测试：`go test -race ./...`
  - 涉及新增依赖或修改构建配置时，必须验证零 CGO 编译通过：`CGO_ENABLED=0 go build ./...`
  - 严禁把未运行或跳过的检查说成已通过。

## Git 与工作区纪律

- 保留并绕开工作区中用户已有的无关未提交改动。
- 未经用户明确授权，禁止执行 `git commit`、`git push`、`git tag` 或破坏性重置命令（`git reset --hard`、`git checkout --`）。
- 暂存文件时必须显式列出路径，禁止 `git add .` 或 `git add -A`。
- 提交前检查 `git status` 与 `git diff`，确认无调试残留、临时文件或敏感凭据混入。
