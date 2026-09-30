# 第3C1段：Linux隔离探测与有界媒体资讯

源码提交 `27f66b1eb36c4d6aed0471b58371bbd6c83aaac0`，基于[第3B2 PR #4](https://github.com/MoYuanCN/Jelee/pull/4)。本段交付隔离基础与规范化 adapter；下一段交付持久快取，再接入扫描。schema仍是3，扫描目前仍只盘点，不调用媒体工具。

## 实际行为

- [Linux helper](media-sandbox.md)：Landlock ABI≥3、架构检查的seccomp、只读已开FD0、固定argv、受限环境及CPU/内存/FD/线程限额；拒绝跨文件内容访问、写入、网络、外部信号及另起程序。只有线程形式clone可用。
- [生产注册](../internal/platform/proberuntime/runtime.go)：固定`/usr/lib/jelee/ffprobe`和嵌入清单的8个Debian动态库；注册与helper重复核对SHA、ELF依赖闭包、root拥有的文件/祖先及调用者不可写。当前Jelee可执行文件也必须受保护。生产helper不接受其他路径或外部policy。
- `process.NewIsolatedFFprobe`只有`ffprobe/metadata`，普通工厂仍拒绝任意程序。helper64/78和异常退出是工具故障；仅ffprobe exit1可作坏媒体，失败输出始终为空。
- [解析与adapter](media-metadata.md)：最多4MiB JSON、64 streams、256 chapters、16层；拒绝重复key、无效数字/溢位/资源超限；仅保留白名单字段。成功后再检查stat、首尾最多128KiB指纹及当前路径的文件身份，改变时丢弃候选结果。
- `jelee-cli doctor probe`不依赖DB；用故意无效的健康输入验证受限helper真的执行到ffprobe，只有预期exit1才报告available。工具/库/内核/暂存/清理失败报告disabled。Windows明确不支持此生产隔离profile。
- Dockerfile建置前需要显式bootstrap-media/runtime。只复制ffprobe、8个动态库、7份相关许可；按嵌入manifest校验全部16文件，并拒绝额外文件/目录。运行镜像保留非root、无shell/SDK/ffmpeg，文件只读、目录0555。未发布镜像或标签。

Landlock对预先打开的FD保留读权限，内容访问授权尽量缩到单个文件；seccomp另限制网络与系统调用。设计按[Linux Landlock文档](https://docs.kernel.org/userspace-api/landlock.html)和[seccomp文档](https://docs.kernel.org/userspace-api/seccomp_filter.html)核对，具体边界由下面真实测试证明。

## 精确来源与验证

完整Windows/Linux验收使用Git索引树 `ed3d3b3ca4c2c5b85832a8e8c5499ae6dae7a2fe`，不含下一段cache草案。最终树 `308cae47232657701a9e9d78a8a514fc7a588d4b` 只增加tools拒绝测试；生产源码、嵌入清单、Docker、SDK/模块身份相同，tools已在两平台单独复验96.7%。公开日志省略本机路径/凭据。

| 验证 | 实际结果 |
| --- | --- |
| [Windows完整快照](evidence/probe-windows.txt) | 三build、全套vet/test、required真实素材/parser、工具身份和DB无关CLI均通过。25 skip=14个PG未配置+11个symlink权限；required tag无skip。Windowsrace没有兼容C编译器，未执行 |
| [Windows最初失败](evidence/probe-windows-longpath-failure.txt)、[路径对照](evidence/probe-windows-path-lengths.txt) | 实测长工作目录259–260 UTF-16字符导致CreateProcessW启动失败；短225–227字符同一源码全部通过。未修改全局系统设置或Go/t.TempDir，原始失败保留。部署须采用足够短的工具/暂存路径；该[微软已记录限制](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-setcurrentdirectory)没有被宣称已修复 |
| [Linux完整race+真PG](evidence/probe-linux-race.txt) | 原生临时项目三build/vet/fullrace/实际PG/requiredtag均通过。完整suite中5个kernel专测因为缺fixture/protected profile/共享UID线程预算skip，加1个optional真tool；另行必需native容器全部执行，optional真tool随后required通过。scanner/input无skip，临时项目清理 |
| [原生内核边界](evidence/sandbox-native.txt) | 纯Go+syscall-only asm的专用UID容器，25项文件/网络/FD/信号/线程边界、root文件/祖先/self保护、ELF/晚期exec错误及真固定ffprobe均通过，0 skip。实际线程达到EAGAIN后全部回收；外层seccomp仅在此专用测试关闭以检验helper自身规则，生产容器使用正常外层策略 |
| [生产profile镜像](evidence/probe-runtime-image.txt) | 镜像 `sha256:1a95e60d62bff36eddba5126f57c67f8bebdd788c57a942386017ac0b23f5b8d`。16固定文件hash通过；只读媒体FD→正式helper→adapter→parser：3个有效MP4/MKV，损坏MKV及2个引用攻击，共6case通过。缺工具/改库/禁Landlock全部disabled，原SHA不变，测试image/container/temp已清理；基础实验镜像保留 |
| [真素材Windows](evidence/parser-real-windows.txt)、[Linux](evidence/parser-real-linux.txt) | MP4 H.264/AAC、24fps/两种尺寸；MKV 2音轨/2SubRip语言/2chapters；大小/时长及来源SHA准确，损坏输出为空。测试仅使用原创素材，不推断播放或特殊codec支持 |
| [Windowsfuzz](evidence/parser-fuzz-windows.txt)、[Linuxfuzz](evidence/parser-fuzz-linux.txt) | 各20秒有界fuzz通过，626815/11039 executions；纯parser覆盖326/334=97.6%。未把HDR/DV/Atmos合成JSON当真实素材 |
| [工具包](evidence/runtime-tools.txt) | manifest先登记归档/源码/许可，再下载校验、抽取并登记文件hash后才执行；34个安全测试和实际离线verify/source archive校验通过。没有apt/全局安装/执行package scripts |

关键覆盖率：Windowsprobe93.8%、process86.0%、runtime-image90.8%；Linux完整race probe94.4%、process89.6%、tools81.7%、runtime-image92.3%。最终增加tools拒绝测试后，使用 `go test -count=1 -cover ./tools` 和 Linux `go test -race -count=1 -cover ./tools` 单独复验，两平台tools均96.7%，见[Windows日志](evidence/tools-runtime-windows.txt)与[Linux日志](evidence/tools-runtime-linux.txt)。CLI73.9%、旧runtime52.5%、PG75.4%等仍未达整个项目门槛。

原生sandbox父计数64.2%、真实晚期kernel exec失败child62.1%，实际atomic计数合并86.5%；成功exec行为有实测，没有假造替换后helper计数。proberuntime普通Linuxrace71.8%，与[9个实际镜像场景](evidence/proberuntime-image-coverage.txt)合并87.1%（74/85）；逐块验证真实counter加和，CLI不入分母，成功exec child不作计数主张。证据[JSON](evidence/proberuntime-image-coverage.json)保存当时源码SHA；其parentHEADAtMeasurement不是实现提交，实际实现归属本报告顶部提交。

重现：`make bootstrap tools-verify bootstrap-media media-tools-verify bootstrap-runtime runtime-tools-verify fixtures fixtures-test runtime-toolchain-test sandbox-test probe-runtime-test`。需要已有Linux amd64 Docker/内核能力，后两项fail closed、不允许skip通过。增量品牌/忽略/diff检查通过；[PR4远端功能CI](https://github.com/MoYuanCN/Jelee/actions/runs/36769288418)通过，完整遗留品牌门禁仍失败。

[PR #5](https://github.com/MoYuanCN/Jelee/pull/5) 的首轮 [Linux CI](https://github.com/MoYuanCN/Jelee/actions/runs/36777560060/job/110099199532) 在 runtime 冷下载阶段失败，尚未执行后续测试。独立空缓存复现实验确认三个旧 GNU 网页端点不可达；已改用官方 GNU FTP HTTPS 来源，全部大小、SHA256 和许可内容不变，真实冷安装、离线验证与36项安装器测试通过，见[修复证据](evidence/runtime-bootstrap-cold.txt)。原 CI 通用日志没有标识具体失败资源，因此独立复现不当作原日志中的细节；新远端运行结果仍须实际回填。此修复没有改变上述 Go 快照或生产工具/库字节。

## 尚未完成的要求

数据库tool_versions/probe_cache、去重租约、失效/重建/清理、扫描/NFO整合和1000→0→K验收接着3C2；Windows隔离、特殊codec真实素材、MediaInfo/mkv工具、崩溃残留清理、完整大规模/24h稳态仍待交付。所有336需求不会因本段通过而标为全部完成。

快速指纹不是全文件快照：同inode未取样中段修改且还原mtime仍可能通过。Landlock不隐藏全部stat/readlink/system metadata；受信root host的原inode写入不在低权限文件保护内，部署必须保持runtime只读且不原地更新。当前实验镜像已纳入许可和Debian源码缓存；BtbN全部静态依赖对应源码、签名与公开分发准备尚未完成，不能称发行就绪。
