# 密码散列与工作量限制

`internal/platform/password` 使用 `golang.org/x/crypto/argon2` 的 Argon2id 实现。模块负责密码格式、PHC 存储格式、散列验证和进程内的计算配额；账户查询、会话、登录限速与权限由调用层处理。

## 参数与兼容范围

默认值为 64 MiB 内存、3 次迭代、并行度 2，同时最多运行 2 次派生。内存单位是 KiB。Argon2id 的实现和参数语义以 [Go 官方包文档](https://pkg.go.dev/golang.org/x/crypto/argon2)为准；当前依赖固定为 `golang.org/x/crypto v0.57.0`。本模块的最低可接受参数为 19 MiB、2 次迭代、并行度 1，符合 [OWASP 密码存储指南](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)列出的 Argon2id 最低配置。

| 配置字段 / JSON 名称 | 默认 | 允许范围 |
| --- | --- | --- |
| `MemoryKiB` / `memoryKiB` | 65536 | 19456–131072 |
| `Iterations` / `iterations` | 3 | 2–6 |
| `Parallelism` / `parallelism` | 2 | 1–4 |
| `MaxConcurrent` / `maxConcurrent` | 2 | 1–8 |

`Config.Validate()` 和 `New(Config)` 均拒绝越界配置，不把零值自动解释为默认值。调用者先取 `DefaultConfig()`，再覆盖配置项。

每个密码生成独立的 16 字节随机盐与 32 字节派生值，盐来自 `crypto/rand`。存储格式严格为：

```text
$argon2id$v=19$m=65536,t=3,p=2$<无填充标准 Base64 盐>$<无填充标准 Base64 派生值>
```

验证前检查 PHC 总长不超过 128 字节，算法、版本、参数顺序、无前导零十进制表示、固定盐与派生值长度，以及规范 Base64 编码。参数的上述硬上限同样适用于数据库中读取的散列，检查发生在分配 Argon2 内存之前。额外字段、换行、填充、溢出、非规范编码或其他算法均返回 `ErrInvalidHash`。其他盐长度、旧算法和旧版 Argon2 需要另行设计显式迁移，不能直接导入后假定兼容。

## 密码输入

- 创建或修改密码：12–1024 **UTF-8 字节**，由 `ValidatePassword` 和 `Hash` 检查。
- 登录验证与未知账户的占位验证：0–1024 UTF-8 字节；空值或短猜测仍执行正常派生工作。
- 不裁剪空格、不做 Unicode 归一化、不强制大小写或字符组合；空格和 NUL 按原字节参与散列。
- 无效 UTF-8 和超长输入直接拒绝。HTTP 层还需在读取请求时限制请求体大小。

`Verify` 仅对固定长度派生值使用常量时间比较。错误密码返回 `(false, nil)`；输入或存储格式错误返回固定错误码。格式检查和不同参数的派生耗时并非恒定，因此不能把这一比较函数理解为整个登录过程恒定时间。

## API 与调用规则

```go
hasher, err := password.New(password.DefaultConfig())
encoded, err := hasher.Hash(ctx, newPassword)
matched, err := hasher.Verify(ctx, suppliedPassword, encoded)
err = hasher.DummyVerify(ctx, suppliedPassword)
```

一个进程中的创建、修改、登录和占位验证应共享同一个 `*Hasher`，才能共享 `MaxConcurrent` 限额。`New` 返回后实例可并发使用；零值实例和 nil 上下文会被拒绝。

未知账户应调用 `DummyVerify`，然后与错误密码返回相同的公开认证失败。它使用随机生成的占位盐和期望值，并执行当前配置的完整派生与比较；比较结果不作为认证结果。数据库查询、缓存或旧散列参数不同仍可能形成时间差，本模块不宣称彻底消除用户名枚举。

成功验证后，调用者可检查 `NeedsRehash`。它只在当前内存和迭代次数均不低于存储值、且至少一项更高时建议重算。仅并行度不同、一个参数升高而另一个降低、或 PHC 无效时返回 false。密码重算和数据库替换由调用者在确认身份后执行。

公开错误为 `ErrInvalidConfig`、`ErrInvalidPassword`、`ErrInvalidHash`、`ErrInvalidContext` 和 `ErrEntropy`；取消或超时保留 `context.Canceled` / `context.DeadlineExceeded`。错误消息不含密码、盐、散列或底层随机源异常。调用者同样不能把这些敏感值写入日志。

## 并发、取消与内存

等待配额时可被上下文取消；获得配额后会再检查一次上下文。Argon2 的 `IDKey` 没有可中断的上下文 API，已开始的派生必须同步运行完毕，再返回取消错误。配额一直保留到派生完成，不启动脱离请求的后台派生。取消不能立即终止正在执行的 CPU 与内存工作。

默认同时最多两次派生，Argon2 工作内存合计约 128 MiB；最高配置可达约 1 GiB。垃圾回收、请求及其他对象还会增加占用，这些值不是进程 RSS 的硬上限。存储散列可以使用配置允许的更高成本，因此部署预算应考虑允许的最大存储成本，而不能只按新散列的默认值计算。

配额约束执行中的工作，未限制等待请求的数量。服务层必须结合请求期限、登录限速和请求并发限制控制排队。模块会尽力清除自己建立的密码字节副本与派生结果；Go 字符串、调用者副本和 Argon2 内部内存不提供完整可证明的清零保证。

## 验证与性能样本

测试覆盖随机盐与官方实现互通、正确/错误密码、Unicode/空格/NUL/长度边界、恶意 PHC 与整数溢出、占位验证、配额上限、排队取消、执行中取消后配额释放，以及随机源失败。解析器 fuzz 仅测试格式与资源边界，不对任意 fuzz 输入执行昂贵派生。

2026-10-01，本地 Go 1.27.1、Windows amd64、AMD Ryzen 7 9850X3D、`GOMAXPROCS=16`，默认参数下各取 20 次操作：

| 基准 | 样本结果 | 分配量 |
| --- | --- | --- |
| `BenchmarkHashDefault` | 31.56 ms/op | 67,116,986 B/op，62 allocs/op |
| `BenchmarkVerifyDefaultConcurrent` | 19.32 ms/op | 67,114,896 B/op，58 allocs/op |

并发基准共享两次派生的配额，其 `ns/op` 是总墙钟时间除以完成数，表示此样本的吞吐成本；它不是单次登录延迟或 P95。样本不构成生产性能承诺，部署应结合实际 CPU、内存、容器限额与并发请求重新测量。

复现命令：

```powershell
./scripts/run-go.ps1 test ./internal/platform/password -count=1 -cover
./scripts/run-go.ps1 vet ./internal/platform/password
./scripts/run-go.ps1 test ./internal/platform/password -run '^$' -bench 'Benchmark(HashDefault|VerifyDefaultConcurrent)$' -benchtime=20x -benchmem
./scripts/run-go.ps1 test ./internal/platform/password -run '^$' -fuzz FuzzPHCParserBounds -fuzztime=5s
```

Linux 的 `.bin/go test -race -count=1 ./internal/platform/password` 已通过（3.110 秒），检查共享配额和取消路径的数据竞争。Windows 专测覆盖率为 95.9%；5 秒解析器 fuzz 执行 4,346,270 次并通过。它们不能代替服务层的认证、限速和权限验收。
