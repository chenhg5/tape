# Tape 技术架构文档

> **Tape — record, search and replay your AI coding sessions.**
>
> Nothing gets lost on tape.

状态:草案 v0.1(设计阶段,未开始编码)

---

## 1. 背景与定位

开发者与 coding agent(Claude Code、Codex、Cursor 等)的会话,是花费了大量时间和金钱沉淀下来的项目知识资产。但今天这些会话:

- **分散**:每个 agent 用自己的私有格式存在本机不同位置;
- **易失**:换机器、换账号、被封号、清缓存,历史就没了;
- **不可检索**:无法回答"三个月前为什么决定不用 OAuth2"这种问题;
- **被锁定**:Claude Code 聊到一半额度用完,无法带着上下文切到 Codex。

Tape 把这些会话当作**用户拥有的、第一公民的数据资产**来对待:统一采集、归档、检索、备份、提炼、迁移。

一句话定位:**Git 管代码,Tape 管会话。**

### 1.1 核心原则

1. **原始数据是 source of truth**。任何摘要、记忆、索引都是从原始会话派生的,可随时重新生成;原始会话永不有损改写。
2. **能力即接口**。每个核心能力都定义为 Go interface,允许多种实现(provider/strategy),新增 agent 或新增备份方式不改动核心。
3. **高内聚低耦合**。核心领域不依赖任何具体 agent、存储、模型;依赖方向永远是 适配器 → 核心,通过组合根(composition root)装配。
4. **人和 agent 都是用户**。每条命令同时提供人类友好输出和机器友好输出(`--json`),并通过 MCP server 把能力暴露给正在工作的 agent。
5. **离线优先,不强制任何云服务**。LLM 能力优先复用用户本机已有的 agent CLI(白嫖用户已付费的订阅),API 只是可选 provider。

### 1.2 非目标(Non-Goals)

- 不做通用"长期记忆数据库"(Mem0/Zep/Letta 的赛道),Memory 只是 Archive 的派生物;
- 不承诺跨 agent **100% 无损**接续(不同模型的 system prompt、工具语义、上下文窗口不同,做不到也不该承诺);
- 不做实时多机同步(多机场景通过 backup/restore 间接覆盖);
- 第一阶段不做 GUI / Web UI。

---

## 2. 产品能力地图

```
                    ┌─────────────────────────────────┐
                    │  消费层(人 + Agent)             │
                    │  CLI · --json robot mode · MCP   │
                    └───────┬──────────┬──────────────┘
        ┌───────────┬───────┴───┬──────┴────┬───────────┬──────────┐
        │  Search   │  Backup   │  Memory   │  Restore  │  Stats   │
        │  跨会话检索│  备份     │  记忆提取  │  会话恢复  │  统计洞察 │
        └───────────┴───────────┴───────────┴───────────┴──────────┘
                    ┌─────────────────────────────────┐
                    │        Archive(归档,地基)        │
                    │   采集 → 归一化 → 本地归档库 → 索引 │
                    └─────────────────────────────────┘
        ┌───────────────────────────────────────────────┐
        │ Source Providers:claude-code · codex · cursor · gemini · qwen · iflow · aider · opencode │
        │ · gemini-cli · opencode · …(可插拔)            │
        └───────────────────────────────────────────────┘
```

### 2.1 Archive(归档)— 一切能力的地基

- 自动发现本机各 agent 的会话存储,增量采集;
- 归一化为统一会话模型(见 §3),同时**逐字节保留原始文件**;
- 维护本地归档库与索引。

命令:`tape sync`(增量),`tape sync --watch`(常驻监听)。

### 2.2 Search(跨会话检索)

- 全文检索,跨 agent、跨项目、跨时间;
- 过滤维度:agent、项目(cwd / git repo)、角色、时间范围;
- 默认 FTS(BM25),向量语义检索作为可选的二级 provider 留接口。

命令:`tape search "oauth2" --agent claude --since 30d`。

### 2.3 Backup(备份)

- 把归档库推送到外部存储,**目标可插拔**:git 仓库 / S3 兼容 OSS / 本地 tarball / rclone remote;
- 支持压缩(zstd)与可选加密(age);
- **脱敏(redaction)是备份链路的强制环节**(见 §2.7):会话里常含 API key、token、内网地址,直接推 git 是事故。

命令:`tape backup --to git`,`tape backup --to s3 --encrypt`。

### 2.4 Memory(记忆提取)

- 从归档会话中提炼结构化记忆:决策(含被否定的方案)、偏好、约束、待办、已知问题;
- **提取策略可插拔**:
  - `llm`:调用本机 agent CLI(`claude -p` / `codex exec` / `cursor-agent -p`)做总结——复用用户已有订阅,零额外配置;
  - `rule`:无 LLM 的规则式降级(提取用户消息、文件改动清单、标题);
- 维度可选:按项目 / 按时间窗口 / 按 agent;
- 产出为 Markdown(`MEMORY.md`、`memory/2026-06.md`),Memory 永远从 Archive 派生,可重复再生成。

命令:`tape memory --project . --strategy llm`。

### 2.5 Restore(会话恢复 / 跨 agent 迁移)

核心场景:Claude Code 聊完(或额度耗尽),想在 Codex 接着干。**恢复策略可插拔**,按保真度分层:

| 策略 | 做法 | 保真度 | 适用 |
|---|---|---|---|
| `native` | 把会话转写成目标 agent 的原生 session 格式,打印 `codex resume <id>` 之类的原生续聊命令 | 高 | 目标 agent 格式可写(claude/codex 先行) |
| `brief` | 生成交接文档(目标/决策/进展/待办/相关文件),注入目标 agent 的首条 prompt | 中 | 任意目标,最稳 |
| `transcript` | 导出(可截断的)对话转写,作为上下文喂给目标 | 低成本 | 短会话 |

命令:`tape restore <session-id> --to codex`(默认自动选策略),别名 `tape rewind`。

### 2.6 Stats(统计洞察)【建议新增】

归档数据天然能回答:各 agent 用量趋势、token/消息量、活跃项目、会话时长分布。对重度用户是"年度报告"式的有趣功能,实现成本低(索引里都有)。

命令:`tape stats [--since 90d]`。

### 2.7 横切能力(建议新增,不单独成命令但影响架构)

- **Redact(脱敏)**:基于规则(正则 + 熵检测)识别 secret,备份前强制扫描,`tape backup --no-redact` 显式跳过;
- **Export(导出分享)**:单个会话渲染为 Markdown/HTML 单文件,`tape show <id> --format html`;
- **Ask(问答)**:`tape ask "为什么不用 OAuth2"` = search 召回 + LLM 综合回答,是 Search 和 Memory 的自然组合,放在 M4;
- **MCP Server**:`tape serve --mcp`,把 `search_sessions` / `get_session` / `save_handoff` 暴露为 MCP 工具,让正在工作的任意 agent 直接查历史——这是"提供给 agent 用"的终极形态。

---

## 3. 领域模型(统一会话模型,IR)

基于对三家真实格式的考察设计(Claude Code:带 `parentUuid` 消息树的多行类型 JSONL;Codex:带 `session_meta` 头的 rollout JSONL;Cursor CLI:每会话一个 SQLite `store.db`):

```go
package model

type Session struct {
    ID        string        // tape 内全局唯一:<agent>/<source-session-id>
    Agent     string        // "claude-code" | "codex" | "cursor" | ...
    SourceID  string        // 原 agent 内的 session id
    Title     string
    CWD       string        // 工作目录(项目归属的主依据)
    GitRepo   string        // 归一化的 repo 标识(可空)
    GitBranch string
    Model     string        // 主用模型(可空)
    StartedAt time.Time
    UpdatedAt time.Time
    Messages  []Message
    Meta      map[string]string // agent 特有但有用的元数据,平铺保留
}

type Message struct {
    ID        string
    ParentID  string          // 保留 Claude 的树结构;线性格式置空
    Role      Role            // user | assistant | tool | system
    Text      string          // 提取后的纯文本(用于索引和展示)
    ToolCalls []ToolCall      // 工具名 + 摘要化的输入输出
    Files     []string        // 该消息涉及的文件路径
    Timestamp time.Time
    Raw       json.RawMessage // 无损保留原始记录 ← 防有损的兜底
}

type ToolCall struct {
    Name   string
    Input  string // 截断的摘要
    Output string // 截断的摘要
}
```

关键决策:

1. **双轨存储**:归档库同时保存「原始文件逐字节副本」和「归一化 IR」。IR 解析有 bug 或格式升级时,随时从 raw 重建,raw 永不重写。
2. **fail-soft 解析**:逐行解析,未知行类型/未知字段不报错,塞进 `Raw` 与 `Meta` 保留。agent 格式迭代快,解析器必须容忍未知。
3. **项目归属**用 `CWD` + git remote 归一化(同一仓库不同路径视为同项目),这是跨 agent 聚合的关键键。

### 3.1 归档库磁盘布局

```
~/.tape/
├── config.toml
├── archive/
│   └── <agent>/
│       └── <project-slug>/
│           └── <session-id>/
│               ├── raw/              # 原始文件逐字节副本(source of truth)
│               │   └── session.jsonl(或 store.db)
│               ├── session.json      # 归一化 IR(可再生)
│               └── meta.json         # 采集时间、源路径、blake3 校验和
├── index/
│   └── tape.db                       # SQLite(元数据 + FTS5 索引,可再生)
└── memory/                           # 提取产物(可再生)
```

`raw/` 是唯一不可再生的部分;`session.json`、`index/`、`memory/` 全部可由 raw 重建(`tape rebuild`)。备份只需保证 raw + meta 完整。

设计立场:**文件是本体,数据库只是索引**。归一化数据落成普通文件而非存进 SQLite,因为备份是核心能力——文件天然对 git/rsync/tar 友好、人肉可读、grep 可达,且永不被库 schema 迁移绑架(对照:cass 把归一化本体放在 SQLite,为此维护了大量 salvage/repair 逻辑)。`meta.json` 记录源文件的 blake3 校验和,`sync` 时用 mtime + 校验和做增量判断与去重(借鉴 cass raw-mirror 的内容寻址思路)。

### 3.2 检索与分词设计

检索分两层,纯 grep 不够(没有排名、没有字段组合过滤、不能支撑 stats/ask 的聚合),但也不需要引入独立搜索引擎:

1. **结构化过滤**:SQLite 普通表(agent、项目、时间、角色),SQL 直查;
2. **全文检索**:SQLite FTS5(BM25 排名),但**分词在 Go 侧注入前自做**:
   - 拉丁文按词小写化;
   - **CJK 连续段切成重叠 bigram**("会话备份" → `会话 话备 备份`),查询侧做同样变换并用 phrase 语义匹配。

CJK bigram 是 Lucene CJKAnalyzer 的经典做法:无词典、无外部依赖、对中英混排稳定。这是相对 cass 的硬差异化——cass 的 FTS5 用 `tokenize='porter'`(英文词干化),中文整段被当作单个 token,中文检索基本不可用。

另保留两个旁路:

- `tape search --raw`:直接顺序扫描归一化文件(grep 式),作为索引损坏/未建时的逃生通道;
- 向量语义检索:`Index` 接口的第二实现位,MVP 不做。cass 为此引入了 ONNX runtime、模型分发、daemon 和两级精排,复杂度占其仓库小半,投入产出不成比例。

---

## 4. 架构与模块边界

采用六边形架构(ports & adapters):核心领域定义模型与端口接口,所有具体技术(agent 格式、存储、LLM、索引)都是外围适配器。

```
                         ┌──────────────────────────┐
                         │   cmd/tape(组合根)        │
                         │   装配 provider,注入依赖   │
                         └────────────┬─────────────┘
                         ┌────────────┴─────────────┐
                         │  internal/cli  internal/mcp │   ← 交付层
                         └────────────┬─────────────┘
                         ┌────────────┴─────────────┐
                         │  internal/core            │   ← 核心(无外部依赖)
                         │   model/  领域模型         │
                         │   ports/  全部接口定义      │
                         │   service/ 用例编排        │
                         └────────────┬─────────────┘
            ┌──────────┬─────────────┼────────────┬───────────┐
   internal/source  internal/index  internal/backup  internal/llm  ...  ← 适配器
   (claude/codex/   (sqlite-fts)    (git/s3/tar)     (claudecli/
    cursor/...)                                      codexcli/api)
```

**依赖规则(强制)**:`core` 不 import 任何适配器;适配器只 import `core`;适配器之间互不 import;只有 `cmd/tape` 知道全部具体类型。用 `go vet` + depguard lint 在 CI 里卡死。

### 4.1 端口定义(core/ports)

```go
// ── 采集 ────────────────────────────────────────────
type Source interface {
    Name() string                                   // "claude-code"
    Detect(ctx context.Context) (Installed bool, DataDir string, err error)
    List(ctx context.Context, since time.Time) ([]SessionRef, error)
    Load(ctx context.Context, ref SessionRef) (*model.Session, RawFiles, error)
}

// 仅 restore 目标需要实现;实现了它的 Source 才能作为 --to 目标
type SessionWriter interface {
    Write(ctx context.Context, s *model.Session) (resumeCmd string, err error)
}

// ── 归档与索引 ──────────────────────────────────────
type Archive interface {
    Put(ctx context.Context, s *model.Session, raw RawFiles) error
    Get(ctx context.Context, id string) (*model.Session, error)
    List(ctx context.Context, f Filter) ([]model.SessionSummary, error)
}

type Index interface {
    Upsert(ctx context.Context, s *model.Session) error
    Search(ctx context.Context, q Query) ([]Hit, error)   // Query 含全文+过滤维度
}

// ── 备份 ────────────────────────────────────────────
type BackupTarget interface {
    Name() string                                   // "git" | "s3" | "tar"
    Push(ctx context.Context, snapshot ArchiveSnapshot, opt BackupOpts) error
    Pull(ctx context.Context, opt BackupOpts) (ArchiveSnapshot, error) // 换机恢复
}

type Redactor interface {
    Scan(data []byte) []Finding
    Redact(data []byte) []byte
}

// ── 记忆提取 ────────────────────────────────────────
type Extractor interface {
    Name() string                                   // "llm" | "rule"
    Extract(ctx context.Context, sessions []*model.Session, opt ExtractOpts) (*Memory, error)
}

// ── 恢复 ────────────────────────────────────────────
type RestoreStrategy interface {
    Name() string                                   // "native" | "brief" | "transcript"
    Applicable(target Source) bool
    Restore(ctx context.Context, s *model.Session, target Source) (*RestoreResult, error)
}

// ── LLM 运行时(被 Extractor/Ask/brief-restore 复用)──
type LLMRunner interface {
    Name() string                                   // "claude-cli" | "codex-cli" | "api"
    Available(ctx context.Context) bool
    Run(ctx context.Context, prompt string, opt RunOpts) (string, error)
}
```

注册机制:每类 provider 一个简单的注册表(显式 `Register()`,在组合根装配,不用 init 魔法),`tape providers` 可列出全部已装载实现及其可用状态。

### 4.2 目录结构(Go)

```
tape/
├── cmd/tape/main.go          # 组合根:读配置、装配 provider、启动 CLI
├── internal/
│   ├── core/
│   │   ├── model/            # Session/Message/Memory/...
│   │   ├── ports/            # 上述全部接口
│   │   └── service/          # 用例编排:SyncService、SearchService、
│   │                         #   BackupService、MemoryService、RestoreService
│   ├── cli/                  # cobra 命令 + 渲染(human/--json 双输出)
│   ├── mcp/                  # MCP server(M4)
│   ├── source/
│   │   ├── claudecode/       # 各自包含:发现、解析、(可选)写回、fixture 测试
│   │   ├── codex/
│   │   └── cursor/
│   ├── archive/local/        # 文件系统归档库
│   ├── index/sqlitefts/      # modernc.org/sqlite(纯 Go,无 cgo)+ FTS5
│   ├── backup/{git,s3,tar}/
│   ├── redact/
│   ├── extract/{llm,rule}/
│   ├── restore/{native,brief,transcript}/
│   └── llm/{claudecli,codexcli,cursorcli,api}/
└── testdata/                 # 各 agent 真实(脱敏后)会话 fixture,golden 测试
```

### 4.3 关键数据流

**sync**:`Source.List(since)` → 增量比对 meta 校验和 → `Source.Load` → `Archive.Put`(raw + IR)→ `Index.Upsert`。单 agent 解析失败不阻塞其他 agent;单文件失败不阻塞批次,失败项记入 sync report。

**restore**:`Archive.Get` → 选策略(显式指定,或按 `Applicable` + 配置优先级自动)→ `native`:`SessionWriter.Write` 输出续聊命令;`brief`:`LLMRunner` 生成交接文档 → 写入 `.tape/handoff.md` 并打印目标 agent 的启动命令。

**backup**:构建 snapshot(raw + meta)→ `Redactor.Scan`,有 Finding 时默认替换并输出报告 → zstd 压缩 →(可选 age 加密)→ `BackupTarget.Push`。

---

## 5. CLI 设计

### 5.1 给人:简洁、有趣

```bash
tape sync                       # 采集归档全部 agent 的新会话
tape ls --project .             # 当前项目的会话列表
tape search "oauth2" --since 30d
tape show <id>                  # 回放一个会话(别名:tape play)
tape restore <id> --to codex    # 跨 agent 恢复(别名:tape rewind)
tape memory --project .         # 生成 MEMORY.md
tape backup --to git
tape stats
```

- 主命令用直白动词,趣味做成别名(`play`/`rewind`),不牺牲可发现性;
- `<id>` 支持前缀匹配和 `@last`(最近一次会话)、`@last:claude` 这类速记;
- 零配置可用:`tape sync` 首次运行自动发现本机 agent,无需先写配置。

### 5.2 给 agent:robot mode

这个 CLI 的二等公民才是人,一等公民可能是 agent 自己。约定:

- **全部命令支持 `--json`**:输出稳定 schema 的单个 JSON 对象(含 `schema_version` 字段),无装饰、无进度条;检测到非 TTY 时自动降级为 plain 输出;
- **退出码语义化**:0 成功 / 1 一般错误 / 2 参数错误 / 3 无结果,agent 可据此分支;
- **`tape search --json --limit 5 --fields minimal`** 这类裁剪参数,控制喂给上下文的 token 量;
- **`tape serve --mcp`**(stdio transport):暴露 `search_sessions`、`get_session`、`list_projects`、`save_handoff` 工具;
- 提供 `tape init --agents-md`:向项目 AGENTS.md 追加一段"如何使用 tape 查询历史会话"的说明,让任何 agent 进入项目就知道怎么用。

---

## 6. 工程决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 语言 | Go | 单二进制分发、跨平台、并发采集;作者主力栈 |
| CLI 框架 | cobra | 事实标准,子命令/补全成熟 |
| 索引 | SQLite + FTS5(modernc.org/sqlite),Go 侧自做分词(拉丁词 + CJK bigram) | 纯 Go 无 cgo;单文件;元数据查询和全文检索一库搞定;中文可检索(cass 做不到);向量检索留 `Index` 第二实现 |
| 归一化数据载体 | 普通文件(session.json),不入库 | 备份/可读/可 grep;SQLite 仅作可重建的索引 |
| 压缩/加密 | zstd / age | 均有成熟纯 Go 实现 |
| LLM 默认 provider | 本机 agent CLI 子进程 | 复用用户已付费订阅,零配置零 key;API provider 作补充 |
| 配置 | `~/.tape/config.toml` + 环境变量 `TAPE_*` | 简单直观,robot mode 下环境变量优先 |
| 解析健壮性 | fail-soft + raw 双轨 + golden fixture 测试 | agent 格式无契约、变更频繁,必须假设会坏 |
| 许可证 | MIT | 干净标准(对比 cass 的自造许可证是明确优势) |

### 6.1 风险与对策

1. **上游格式变更**(最大风险):raw 双轨保证数据不丢;fail-soft 保证旧数据仍可用;每个 source 包带版本嗅探 + fixture 回归测试;社区 issue 模板引导用户提交新格式样本。
2. **native restore 的格式写入是逆向工程**:目标 agent 升级可能导致写入的 session 无法 resume。对策:写入后做 read-back 校验;失败自动降级到 `brief` 策略;`native` 标注为 best-effort。
3. **会话含敏感信息**:备份链路强制 redact;归档库本身权限 0700;文档明确告知风险。
4. **Cursor 桌面端(SQLite state.vscdb)格式最不稳定**:M1 先支持 Cursor CLI(`~/.cursor/chats`,格式更简单),桌面端 M2 再做。

---

## 7. 里程碑

| 阶段 | 内容 | 验收 | 状态 |
|---|---|---|---|
| **M1 地基** | core 模型/端口、claude-code + codex + cursor-cli 三个 Source、本地归档、FTS 检索、`sync/ls/search/show`、全量 `--json` | 在作者本机归档并检索全部历史会话 | ✅ 63 会话归档,中英文检索验证 |
| **M2 备份** | Redactor、git + tar 两个 BackupTarget、zstd、`backup/index rebuild`、换机 `pull` 恢复归档 | 私有 git 仓库完成一次全量备份与异机还原 | ✅ 真实归档扫出 83 处 secret 并拦截;980MB→35MB 脱敏导出;异机 clone+重建索引检索成功 |
| **M3 恢复** | `brief` 策略(LLMRunner: claude/codex/cursor CLI)、`native` 策略(claude↔codex 双向)、`restore/rewind` | Claude 会话在 Codex 原生 resume 成功;失败可降级 | ✅ claude→codex 原生 resume 实测成功;brief(codex 总结)与模板降级验证 |
| **M3.5 Phase A 扩源** | Gemini CLI / Qwen Code / iFlow / Aider / OpenCode 五个新 Source;`memory` 策略适配各家 `<AGENT>.md` 约定;品牌色+交互 picker 列表 | 8 家 agent 全部跑通 `sync → search → show → restore (memory/transcript)` | ✅ 全部解析器单测+端到端验证(`TestPhaseAAgents`)，OpenCode 走 Drizzle SQLite (`session/message/part`) |
| **M4 记忆与 agent 化** | Extractor(llm/rule)、`memory`、`ask`、`stats`、MCP server、`init --agents-md` | 任意 agent 通过 MCP 查询历史并拿到交接文档 | 待做 |

M3 后增量交付:

- **SSH 远端同步**(`tape sync --remote user@host`):`internal/remote` 把远端 `$HOME` 下的 agent 会话目录镜像到 `~/.tape/remotes/<host>/`,传输用 `ssh + tar`(远端零依赖,尊重 `~/.ssh/config`);首次全量,之后用 `tar --newer-mtime` 增量(留 1 小时余量,归档 checksum 兜底去重),老 tar 不支持该旗标时自动回退全量。镜像目录复用本地同一套解析器(`App.SourceFactory(home)`),`remote.WrapSource` 给会话打 `meta.host` 标记并在 sync 报告中带 `host` 字段。Go 侧解 tar 流,带路径穿越防护。
- **npm 分发**(`npm/` + `scripts/release-npm.sh`):esbuild 式平台分包——主包(JS launcher,透传语义退出码)通过 `optionalDependencies` 按平台拉取预编译二进制包(5 平台,`os`/`cpu` 字段约束),无 postinstall 下载。`release-npm.sh <version> [latest|beta]` 一键构建+发布,dist-tag 区分正式/beta(`npm i -g agent-tape@beta`);包名经 `NPM_PACKAGE` 可配(npm 上 `tape`、`tape-cli` 已被占,`agent-tape` / `tape-agent` 可用)。

实现中确定的关键细节(对原设计的细化):

- **备份即仓库**:git 目标直接把 `~/.tape/archive` 变成 git 仓库,remote 存在仓库自身的 git config 里,tape 无需额外配置状态;
- **脱敏不改本地**:`backup push` 走"扫描即拦截"(发现 secret 阻断,`--allow-secrets` 显式放行),`backup export` 在写入 tarball 流中替换(`[REDACTED:<rule>]`);SQLite 等二进制文件用等长掩码(`ApplyKeepLength`)避免破坏文件结构;本地归档永不被改写;
- **codex 原生写入的健壮性**:session_meta 由 Rust 端严格反序列化,字段模板取自本机最新真实会话(保证 cli_version 与枚举值匹配当前安装版本),仅覆盖 id/timestamp/cwd;
- **CLI 对 agent 的契约**(参照 agent-cli-guide):非 TTY 默认输出 JSON、尊重 `NO_COLOR`、`--dry-run` 成功退出码 10、错误为机器可读 JSON(type/suggestion/retryable)、`tape schema` 提供命令树自省。

每个里程碑独立可发布、独立有价值(M1 就已经解决"统一归档+检索"的核心痛点)。

### 7.1 测试体系与质量门禁

测试金字塔(`make ci` 一键全跑,CI 在 Linux/macOS 双平台执行):

| 层 | 位置 | 覆盖内容 |
|---|---|---|
| 单元测试 | 各包 `*_test.go` | 纯函数与边界:分词/截断的 UTF-8 安全、UUID 格式、varint、redact 规则逐条触发 + 占位符误报控制、flexInt64 容错 |
| 集成测试 | `archive/local`、`backup/*`、`index/sqlitefts`、`llm` | 真实文件系统/真实 git 二进制/真实 SQLite;LLM runner 用 PATH 桩脚本模拟三家 CLI;sync 编排用 fake 三件套验证错误隔离 |
| E2E | `test/e2e/` | 编译真实二进制,在伪造 `$HOME`(三家 agent 夹具)+ 独立 `TAPE_HOME` 下驱动完整旅程:sync→ls→search(中英文)→show→restore(native 双向/brief/dry-run)→backup(secret 门禁/git 容灾演练/导出脱敏校验),逐一断言退出码、JSON 信封与 stderr 错误对象 |
| 冒烟 | `make smoke`(`go test -short`) | E2E 的快速子集(~1s):help/version/schema/空机 sync/usage 错误 |

门禁(任一失败即阻塞):`gofmt` → `go vet` → `-race` 全量测试 → 单测覆盖率下限(60%)→ 冒烟 → E2E → 五平台交叉编译。E2E 另以 `-cover` 插桩二进制产出进程级覆盖率报告(`make cover-e2e`),与单测覆盖互补(CLI 命令层主要由 E2E 覆盖)。

---

## 8. 开放问题(待定,不阻塞 M1)

- 会话级增量备份的粒度(整库 snapshot vs 按 session 增量)——M2 前决定;
- 向量语义检索的默认开关与模型分发方式(参考 cass 的可选本地模型做法);
- 多机场景是否需要 `tape merge`(合并两份归档库)——视 M2 后反馈;
- Gemini CLI / OpenCode / Aider 等 Source 的优先级——按社区需求排。
