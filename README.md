# PanFind（盘寻）

PanFind 是一个面向云盘的 POSIX 风格文件元数据查询工具。它读取官方客户端保存在本机的文件元数据，将其构建为只读命名空间，并提供接近 GNU `find` 的组合查询能力。

当前版本是百度网盘 Windows 客户端的 MVP：只读取本地 `filecache.db`，不登录百度账号，不调用远端 API，也不执行上传、下载、删除、移动或重命名。

## 当前能力

- 自动发现百度网盘 Windows 客户端的本地账号数据库；
- 支持单账号自动选择和多账号显式选择；
- 使用 SQLite 只读事务加载一致的完整快照；
- 支持路径查找、目录遍历和稳定节点标识；
- 支持文本、JSON Lines 和有限的 `-printf` 输出；
- 支持前台 `watch`，数据库变化后在约一秒内全量刷新；
- 新快照构建成功后原子切换 generation；
- 刷新失败时继续保留上一代完整快照并自动重试；
- 提供适合脚本和 AI Agent 使用的 `schema`、`capabilities` 和 `explain` 命令。

## 环境要求

- Windows 10 或 Windows 11；
- 已安装并使用过百度网盘 Windows 客户端；
- 从源码构建需要 Go 1.24 或更高版本。

PanFind 当前查找的数据库位置为：

```text
%APPDATA%\baidu\BaiduNetdisk\module\BrowserEngine\users\<account-id>\filecache.db
```

## 从源码构建

```powershell
git clone git@github.com:lenovobenben/panfind.git
cd panfind
go build -trimpath -o bin/panfind.exe ./cmd/panfind
```

查看帮助和版本：

```powershell
.\bin\panfind.exe help
.\bin\panfind.exe version
```

项目使用纯 Go SQLite 驱动，构建不要求 CGO。

## 快速开始

查找大于 1 GiB 的文件：

```powershell
panfind baidu:/ -type f -size +1G
```

查找扩展名为 `.exe` 的文件，忽略大小写：

```powershell
panfind baidu:/ -type f -iname "*.exe"
```

组合多个条件：

```powershell
panfind baidu:/ -type f '(' -iname "*.mkv" -o -iname "*.mp4" ')' -size +800M -size -2G
```

只查询某个子目录：

```powershell
panfind baidu:/资料 -type f -newermt 2025-01-01
```

## 账号选择

列出已经发现的账号：

```powershell
panfind accounts
panfind accounts --json
```

只有一个账号时，`query` 和 `watch` 会自动选择该账号。发现多个账号时，需要把 `--account` 紧跟在查询根路径后面：

```powershell
panfind baidu:/ --account <account-id> -type f -size +1G
panfind watch baidu:/ --account <account-id> -type f -iname "*.mkv"
```

账号 ID 来自百度网盘客户端的本地用户目录。第一版不把账号编码进 `baidu:/` 路径。

## 查询语法

当前支持以下 GNU `find` 风格子集：

| 类别 | 语法 |
| --- | --- |
| 类型 | `-type f`、`-type d` |
| 名称 | `-name PATTERN`、`-iname PATTERN` |
| 路径 | `-path PATTERN`、`-ipath PATTERN` |
| 大小 | `-size N[cwbkMG]`，支持 `+N` 和 `-N` |
| 时间 | `-mtime N`、`-newermt DATE` |
| 深度 | `-mindepth N`、`-maxdepth N` |
| 逻辑 | 隐式 AND、`-a`、`-o`、`!`、括号 |
| 动作 | `-print`、终端位置的 `-printf FORMAT` |

运算符优先级为：`!` 高于 AND，AND 高于 OR。`-mindepth` 和 `-maxdepth` 当前必须出现在查询谓词之前。

`-printf` 当前支持：

- `%p`：完整云盘路径；
- `%f`：文件名；
- `%s`：字节大小；
- `%y`：节点类型；
- `%T+`：修改时间；
- `%%`：百分号；
- `\n`、`\t`、`\0`、`\\`：常用转义。

查看机器可读的完整语法和解析结果：

```powershell
panfind schema --json
panfind explain baidu:/ -type f -size +1G --json
```

## 输出

默认每行输出一个云盘路径：

```text
baidu:/电影/example.mkv
```

JSON Lines 输出适合 `jq`、脚本或 Agent 逐条消费：

```powershell
panfind baidu:/ -type f -size +1G --json
```

示例记录：

```json
{"path":"baidu:/电影/example.mkv","type":"file","size":2147483648,"modified_at":"2025-08-01T10:00:00Z"}
```

自定义输出：

```powershell
panfind baidu:/ -type f -printf "%p\t%s\n"
```

## 持续查询

`watch` 在前台维持一个同步会话，并在首代快照以及每次成功发布新 generation 后重新执行同一条查询：

```powershell
panfind watch baidu:/ -type f -iname "*.mkv"
panfind watch baidu:/ -type f -size +1G --json
```

文本模式通过 stderr 报告 generation 和匹配数量。JSON Lines 模式会为每条结果增加 `generation` 字段。每一代输出的是完整匹配集合，不是增量事件；使用 Ctrl+C 结束。

当前实现轮询 `filecache.db`、WAL、SHM 和父目录的变化提示，合并短时间内的突发变化后重新构建完整快照。它不是后台服务，也不提供 IPC 或第二份持久化缓存。

## 状态和能力

```powershell
panfind status
panfind status --json
panfind capabilities
panfind capabilities --json
```

`status` 会发现账号、加载当前快照并显示 generation、节点数、文件数和目录数。`capabilities` 描述当前百度本地数据源能够可靠提供的字段。

## 退出码

| 退出码 | 含义 |
| ---: | --- |
| `0` | 命令成功；一次性查询至少产生一个结果 |
| `1` | 查询合法但没有匹配结果 |
| `2` | 命令、查询、选项或格式错误 |
| `3` | 账号发现、数据库读取、schema 或命名空间错误 |
| `4` | 结果输出失败 |

下游命令正常提前关闭管道时，PanFind 按成功处理。

## 只读和隐私边界

PanFind 将用户文件名、路径、大小、哈希和时间视为敏感数据。当前版本：

- 只读取官方客户端的本地 SQLite 数据库；
- 使用 `mode=ro`、`query_only` 和只读事务；
- 不向官方数据库添加表、索引或触发器；
- 不修改百度网盘客户端配置；
- 不要求 BDUSS、Cookie、账号密码或开发者密钥；
- 不上传文件元数据，也不包含遥测功能。

官方客户端关闭后，本地数据库可能不会包含其他设备刚刚产生的云端变化。PanFind 展示的是本地缓存视图，不应被理解为实时远端视图。

`filecache.db` 是百度网盘客户端的内部实现，并非稳定公开协议。客户端升级可能改变路径或表结构；PanFind 遇到不支持的 schema 时会明确失败，不返回貌似正确的结果。

## 当前限制

- 仅支持百度网盘 Windows 客户端的本地元数据；
- 不支持上传、下载、删除、移动、重命名和文件内容搜索；
- 不支持 GUI、后台守护进程和远端 API；
- 不提供可靠的创建时间或加入网盘时间；
- `watch` 会重复输出每代完整结果，而不是输出差异；
- 尚未承诺兼容 GNU `find` 未列出的选项和细节；
- 尚未提供预编译 Release，当前需要从源码构建。

## 开发和验证

运行测试和静态检查：

```powershell
go test ./...
go vet ./...
```

运行基准：

```powershell
go test -bench=. -benchmem ./internal/namespace ./internal/query
```

项目已经使用真实的约 1.8 万节点百度缓存进行只读验证，并包含 10 万和 100 万节点合成基准。受控 SQLite 集成测试覆盖了 WAL 写入、增删改移、无效数据、排他锁、失败保留和恢复刷新。下一阶段主要工作是 GNU `find` 差分测试和 Windows 发布流程，不继续提前优化内存或引入增量同步。

## 许可证

PanFind 使用 [MIT License](LICENSE)。
