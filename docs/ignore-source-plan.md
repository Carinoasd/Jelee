# 3D1B：安全来源观察与编译缓存

状态：实现及本地最终验证完成，Windows和原生Linux各29包通过；详细报告随证据提交记录。前段纯matcher已发布[PR #11](https://github.com/MoYuanCN/Jelee/pull/11)，源码 `9afc8f15c6ca3b190bc6eafdb7e24356ad1cd33c`，验证HEAD `32e3c85ca3a010641858007d11382557cb2fe7ac`。本段分支 `feat/jelee-ignore-source`。

## 范围与选择

新增 `internal/adapter/media/ignore`，包名 `ignoresource`。它在可信配置根内观察某个候选的可达祖先规则，调用已经验证的纯matcher；不接scanner、DB、runtime、HTTP或旧格式。用户已经授权按计划逐段验证、提交和PR，常规实现选择依此继续。

仅mtime/size缓存不能识别恢复时间戳的等长改文，也不能安全复用不存在来源的记录；本段选择每次重新探测来源、完整读取/hash，缓存只省Compile。直接复用NFO reader虽能限制根边界，仍允许根内链接；本段沿用其身份核对和取消/关闭模式，另建严格原生开檔层，不改现有NFO或scanner。

## API与发现顺序

```go
func NewResolver() *Resolver
func (*Resolver) Evaluate(
    ctx context.Context, trustedRoot, candidate string,
    kind ignore.Kind, options ignore.Options,
) (Observation, error)
```

trustedRoot只能来自可信本地配置/catalog边界，不是可直接交给HTTP用户的路径参数。候选采用纯matcher的规范根相对路径合同。Observation含Match、诊断副本和不含原文的规则集合token；不公开Program、FD或原始文本，避免把一条祖先链当成任意后代的完整规则集合。

对 `a/b/file`：读根规则、判断a、可达才开a并读其规则、判断a/b、可达才开b并读其规则，最后判断file。目录自身不加载其内部规则；被排除后停止访问更深路径。128来源是每候选祖先链的上限，不是整个媒体库的来源总数。

只在安全打开的父目录内固定leaf `.jeleeignore` 真正不存在时记录absence。父目录缺失、权限拒绝、链接/reparse、非普通文件、编码/尺寸错误、身份变化均返回固定错误与零Observation；不回退暖缓存中的旧决定。所有根绝对路径、原文及OS错误仅留在私有实现，不能进入公开错误。

## 原生安全开檔

- Linux使用现有x/sys的openat2，根以下相对打开指定BENEATH、NO_SYMLINKS、NO_MAGICLINKS及只读/NONBLOCK/CLOEXEC；根本身也拒绝链接。能力不可用时明确unavailable，不退回会跟随链接的实现。
- Windows使用已有NtCreateFile，以held directory handle作为RootDirectory，指定OBJ_DONT_REPARSE、FILE_OPEN、唯读与同步IO选项，检查handle的全部reparse attributes和普通文件类型。根与目录均做同样核对。
- 固定Go实现的Root.OpenFile会在遇到链接后自行解析，不能只向它传O_NOFOLLOW就声称严格拒绝。
- 原生handle提供固定volume/file identity、size、mtime和kind。根/目录身份从held handle取得，避免路径Stat的Windows延迟file ID。目录handle保留到本次观察结束，防止父目录删除后身份复用。
- 对大小写，采用宿主文件系统实际查找行为，并保留调用方规范相对路径的拼写。模式大小写仍由ignore.Options控制；不把Windows的路径alias当作纯matcher折叠来源名。原生Linux与Windows单独验证。

本段不承诺阻止hardlink、bind mount或多文件原子snapshot。普通open/stat与网络文件系统阻塞不能保证硬取消；这些限制不因此改成不受根约束的fallback。

## 完整性与缓存

首次逐份完整读取并hash，核对held file的前后身份、size/mtime、实际读取长度和合法时间范围。做出决定后，从配置路径严格重开根，再沿已观察链重开目录、复查present/missing；present再次完整读取/hash并比对。检查点仍不等于原子snapshot，也不能防止最后检查之后的新变化。

缓存键包含ProgramVersion、case mode、可信根身份以及有序来源的相对路径、文件身份、size、mtime和完整SHA。absence每次重新观察；未增加来源时可沿用本次已有Program，不能据此跳过下一次磁盘检查。规则集合token还覆盖本次目录链与absence，只识别观察，不代表持久租约或扫描完成授权。

编译得到的前缀Program先保留在本次有界暂存中；全部复查、资源关闭和最后context检查通过后才发布到共享LRU。失败不发布本次新项。提交点之后发生的取消不撤销已经完成的结果。所有cache只保留不可变Program与有界身份记录，不保留原文或FD。

## 固定预算

| 项目 | 限制 |
| --- | ---: |
| admitted Evaluate | 2，满额立即Busy，不创建等待队列 |
| 一次调用共用deadline | 30秒，继承更短的调用方期限 |
| 根路径 | 4,096 bytes；候选/来源使用matcher的1,024 bytes/128组件限制 |
| 单份/首次全部规则原文 | 256KiB / 4MiB，复查另受相同预算 |
| 编译次数/前缀求值次数 | 各至多128次；不因目录层级重设本次deadline |
| 累计交给Compile的重复原文 | 32MiB；超过返回工作量错误 |
| 共享缓存 | 64项，16MiB保守记账权重 |
| 每次未发布暂存 | 同样64项/16MiB；超出按LRU淘汰，超过单项权重则不缓存 |

每个Program权重为4,096 + 各来源(2,048 + 2×来源路径bytes + 64×原文bytes)，保守覆盖小规则和slice容量增长；它不是整个Go进程RSS计量。已有纯matcher每Compile/Evaluate的工作预算仍生效；本段额外限制整次调用次数、累计输入和共用期限，不把单个前缀预算冒称整次预算。LRU锁不包I/O或Compile。

## 实测验收

两平台真实小目录验证冷/暖判定与provenance、原文只读、full-read/hash/Compile计数；相同mtime+size的中段改文、新增此前缺失规则、删除回到祖先、根/父/leaf替换与被剪枝深处不访问。控制文件路径因追加basename而超限时须安全失败，不能静默略过。

Linux在原生/tmp验证内部/外部/祖先symlink、FIFO、不可读和根替换；Windows验证真实reparse/junction及可用的symlink权限，缺失的实际能力须明确列出，不把mock当作原生证明。read开始后取消必须关owned file并join；close失败清空结果。已有暖cache后分别注入故障，证明不回退旧决定。

测试还覆盖并发2项上限、LRU项数/权重、option/root隔离、整次Compile预算、缓存淘汰不改变仍在使用的Program，以及多条链累计超过128份控制文件仍可分别处理。代码不接扫描，3D1C继续负责规则快照/generation、持久fencing与“排除不等于missing”的库存语义。
