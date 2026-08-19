# PanFind（盘寻）

PanFind 是一个面向百度网盘 Windows 和 macOS 客户端本地元数据的只读搜索工具。它把本地文件树组织成类似 POSIX 的路径命名空间，让命令行、脚本和 AI Agent 能用接近 GNU `find` 的方式查找百度网盘文件。

它不登录百度网盘网页，不要求账号密码、Cookie 或开发者密钥，也不会上传、移动、重命名或删除网盘文件。

## 为什么需要 PanFind

网盘文件积累到一定规模后，真正困难的通常不是“下载”，而是“想不起来放在哪里”：

- 文件名可能只有缩写、编号、拼音或错误名称；
- 同一资源可能散落在多层目录、不同分卷和不同版本中；
- 很久以前保存的资料，只记得模糊主题，不记得准确关键词；
- 想按大小、时间、类型或哈希筛选时，客户端搜索能力不够灵活；
- AI Agent 能理解自然语言，却缺少一个稳定、快速、可组合的本地元数据入口。

PanFind 将百度网盘客户端已经保存在本机的元数据加载为完整快照。普通查询只访问本地内存和本地数据库，不需要为了搜索遍历远端文件树。

## 能做什么

### 组合条件搜索

支持接近 GNU `find` 的表达式：

- `-type f|d`
- `-name`、`-iname`
- `-path`、`-ipath`
- `-size`
- `-mtime`、`-newermt`
- `-mindepth`、`-maxdepth`
- `!`、`-a`、`-o` 和括号
- `-print` 与有限的 `-printf`

### 机器可读输出

- JSON Lines 查询结果；
- 机器可读的 `schema`、`capabilities` 和 `explain`；
- 稳定的退出码，方便脚本和 Agent 判断成功、无匹配、参数错误、数据源错误及输出错误；
- 在本地数据提供时输出大小、时间、哈希和稳定节点标识。

### AI Agent 搜索入口

仓库包含自然语言搜索 Skill。Agent 可以先用 PanFind 缩小候选范围，再结合公开资料、文件格式分析或用户授权的内容工具继续判断，最后返回准确的 `baidu:/` 路径、证据和不确定性。

PanFind 负责提供只读元数据命名空间，不限制 Agent 如何理解用户的搜索目标。

## 只读与隐私边界

PanFind 将文件名、路径、大小、哈希和时间视为敏感数据。百度网盘数据路径遵守以下边界：

- 只读取官方 Windows 或 macOS 客户端维护的本地 SQLite 数据库；
- 使用 SQLite `mode=ro`、`query_only` 和只读事务；
- 不修改官方数据库、客户端配置或网盘内容；
- 不要求 BDUSS、Cookie、账号密码或开发者密钥；
- 不上传元数据，不包含遥测；
- 不启动后台服务，不进行隐式自动更新；
- 遇到不支持的数据库结构时明确失败，不返回貌似正确的结果；
- 查询和本地快照加载不需要访问百度网盘远端接口。

不要仅凭项目说明信任任何处理私人元数据的第三方二进制。建议先审查源码和依赖，再自行编译运行。

## 当前已经实现

- 自动发现百度网盘 Windows 和 macOS 客户端的本地账号数据库；
- 单账号自动选择，多账号显式选择；
- 通过只读 SQLite 事务加载一致的完整快照；
- 路径查找、目录遍历、大小、修改时间、哈希和稳定节点标识；
- 接近 GNU `find` 的组合查询；
- JSON Lines 和有限 `-printf` 输出；
- `schema`、`capabilities`、`explain`、`accounts` 和 `status`；
- 前台 `watch`，本地数据库变化后构建并原子发布新快照；
- 刷新失败时保留上一代完整快照并自动重试；
- 可被 Codex 发现的仓库级自然语言搜索 Skill。

## 安装与构建

运行环境：

- Windows 10/11（amd64），或 macOS（Intel amd64 / Apple Silicon arm64）；
- 已安装并使用过对应平台的百度网盘桌面客户端。

从源码构建需要 Go 1.24 或更高版本：

```text
git clone git@github.com:lenovobenben/panfind.git
cd panfind
go test ./...
go build -trimpath ./cmd/panfind
```

GitHub Release 提供 `panfind-windows-amd64.exe`、`panfind-macos-amd64.tar.gz` 和 `panfind-macos-arm64.tar.gz`。macOS 压缩包内保留对应架构的可执行文件名；请选择与 `uname -m` 对应的版本，`x86_64` 使用 `panfind-macos-amd64`，`arm64` 使用 `panfind-macos-arm64`。仓库级 Skill 会自动查找这些名称。

如果选择使用预编译文件，请同时核对发布页提供的 SHA-256 校验值。macOS 预编译文件目前没有 Apple 代码签名和公证；对隐私敏感或受 Gatekeeper 限制的使用场景，建议审查源码后自行构建。

## 基本用法

以下示例假设可执行文件已经位于 `PATH`；也可以在 Windows 使用 `.\panfind-windows-amd64.exe`，在 macOS 使用对应架构的 `./panfind-macos-*`：

```sh
# 查看账号和本地快照状态
panfind accounts --json
panfind status --json

# 查找大于 1 GiB 的文件
panfind baidu:/ -type f -size +1G

# 不区分大小写查找 PDF
panfind baidu:/ -type f -iname "*.pdf"

# 输出 JSON Lines
panfind baidu:/资料 -type f -newermt 2025-01-01 --json

# 自定义输出
panfind baidu:/ -type f -iname "*.mkv" -printf "%s %p\n"

# 查看查询解释和能力
panfind explain baidu:/ -type f -size +1G --json
panfind capabilities --json
panfind schema --json
```

完整参数以程序自身输出为准：

```sh
panfind help
```

## 推荐用法：交给 AI Agent

与其要求用户记住复杂表达式，更推荐直接描述搜索目标，例如：

- “找出我保存过的所有某系列资源，包含可能的英文名和缩写。”
- “找出大于 4GB、最近一年没有修改的视频文件。”
- “我记得是一本南瓜主题的英文 PDF，但忘了目录和文件名。”
- “分析这些目录是不是同一套资源的不同版本。”

Agent 可以多轮调用 PanFind，逐步扩大或缩小范围，并将文件路径与公开知识结合，而不是只做一次文件名关键词匹配。

仓库级 Skill 位于：

```text
.agents/skills/search-baidu-drive/
```

## 当前限制

- 正式运行环境为 Windows amd64、macOS amd64 和 macOS arm64；
- 依赖百度网盘客户端已经生成的本地数据库；
- 本地缓存可能暂时不包含其他设备刚产生的变化；
- `filecache.db` 属于客户端内部实现，PanFind 与客户端版本生成的数据库结构直接绑定；当前已验证基线为 Windows 百度网盘 `8.6.8.102` 和 macOS Intel 百度网盘 `8.7.0 (461)`，未来版本可能改变路径、结构或字段语义，详见[客户端兼容性基线](docs/项目设计草案.md#61-客户端兼容性基线)；
- 不搜索文件正文；
- 不上传、下载、分享、删除、移动或重命名文件；
- 不保证所有客户端版本都提供完整的创建时间或加入网盘时间；
- 网页目录链接依赖未公开的前端路由，只作为定位便利。

## 开发与验证

```text
go test ./...
go vet ./...
```

项目已经使用 Windows 和 macOS 客户端的真实本地缓存进行只读验证，并包含 10 万和 100 万节点合成基准。CI 在 Windows amd64、macOS amd64 和 macOS arm64 原生 runner 上执行测试、静态检查、构建和启动检查。SQLite 集成测试覆盖 WAL 写入、增删改移、无效数据、排他锁、失败保留和恢复刷新；GNU `find` 差分测试用于固定已经承诺的查询语义。

更多背景与实现记录：

- [产品愿景与使用场景](docs/产品愿景与使用场景.md)
- [百度本地数据设计](docs/项目设计草案.md)
- [完整设计与实验记录](docs/项目完整说明.md)

## 风险与责任

本项目按 [MIT License](LICENSE)“原样”提供，不作任何明示或暗示担保。项目文档描述的是设计边界和已完成测试，不构成数据完整性、持续可用性、隐私安全或适用于特定目的的保证。

使用者应自行审查代码、依赖和构建产物，并自行承担运行、修改及使用本软件的风险。

## 许可证

PanFind 使用 [MIT License](LICENSE)。
