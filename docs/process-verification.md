# 第3B2段：程序控制、唯读输入、工具诊断与自建素材

源码提交 `03a559524c0b42abe888e0199b20a8a404853842`，基于[第3B1 PR #3](https://github.com/MoYuanCN/Jelee/pull/3)。本段已交付；下一段继续OS隔离和真实媒体规范化/缓存。

## 行为与边界

- [Runner](process-runner.md)仅接受受信代码注册的固定路径/operation/argv，限制并发、时限与输出；Linux保留进程身份后杀进程组再回收，Windows创建时原子绑定Job Object并限定继承handle。取消、超量输出、子孙进程和正常退出后清理都有实测。
- [输入](probe-input.md)打开受授权根下的只读可seek文件，使用FD0传递；没有把原路径交给子程序重开，没有整档复制。文件替换、符号链接、特殊文件及取消检查都有回归。
- `jelee-cli doctor tools`独立于DB，按嵌入清单验证工具/许可，运行验证后的私有副本`-version`，只输出固定安全摘要。它始终标示`disabled_sandbox`。
- [素材生成](fixtures.md)仅显式开发tag构建，生成13个小型原创媒体/字幕/章节/图片/NFO文件到新目录；已有文件不替换，失败/取消清理本次产物。普通生产构建拒绝ffmpeg注册；环境变量无法开启生成器API。
- Docker建置已补入嵌入身份所需的两个工具源码/清单文件。实际生产镜像仍只有三个静态Go二进制，无ffmpeg/ffprobe/SDK/shell。

## 精确来源与实际结果

验收使用Git索引封装，排除下一段未提交的parser/sandbox原型。Windows全套树 `934cf6f22221c3e2611e05c6a25e2b3309ca2b65` 通过后，树 `4ff6b4c6e49219179389c4a1dd7394759b534aa1`只增加安全测试与精简Make测试调用，生产Go源码、go.mod/sum、嵌入清单逐项相同；受影响部分已复验。Linux完整套件直接使用后者。容器树 `ff04a8c1fedf66639f1d1aaaff56a6fed649406f` 再补Docker COPY，生产Go源码相同。

| 验证 | 实际结果与范围 |
| --- | --- |
| [Windows完整快照](evidence/process-windows-full.txt)、[补测快照](evidence/process-windows.txt) | 三CLI/service build、vet、全套普通test与required媒体测试通过；旧完整套件23 skip：14个PG未配置、9个symlink权限；补测4个symlink权限skip，junction实测通过 |
| [Linux完整race+PG](evidence/process-linux-race.txt) | 原生文件系统的项目临时快照，完整build/vet/race/真实PG通过；完整套件仅真实工具optional测试先skip，之后required真工具测试实际通过；scanner/input无skip。临时项目已清理，SDK只复用主项目安装 |
| 真固定媒体工具 | 两平台MP4/MKV有效与损坏样本、分辨率、AAC/字幕/章节数量、时长/大小及原文件SHA均通过；生成器缺工具必须明确失败 |
| [Linux SIGTERM](evidence/fixtures-sigterm.txt) | 真FFmpeg编码期间取消生成器，0.022秒内退出并回收child、清理本次目录、保留预存文件；POSIX包装器最终binary leaf另有symlink拒绝回归 |
| [生产容器](evidence/process-container.txt) | 镜像 `sha256:b2c7c3b60ab6627ba0d80f7f3bcb1f19c1d9dfcbd7721ad4b9b32562ce25b637`；UID/GID65532、只读根/媒体、cap-drop、no-new-privileges、健康；256文件真盘点、原hash不变、SIGTERM exit0及schema3→0通过；无DB的工具诊断安全报告missing/disabled。测试schema/容器/凭据已清理 |

本段关键包覆盖率：Windows runner86.0%、input85.9%、identity89.8%、tools86.7%；Linux完整race runner87.9%、input88.9%、identity86.0%、tools86.7%。CLI72.4%、PG75.4%、runtime52.5%；不能宣称整个项目覆盖率门槛已全部满足。Windowsrace仍缺兼容C编译器，未执行。

新增[现有host编译器登记](evidence/host-compiler.txt)。此前Linuxrace实际结果保留，但当时host compiler未列manifest；本段登记后先核对5个ELF/2份copyright哈希，再执行最终Linuxrace。没有安装新全局compiler或修改全局Git身份。

第3B1[远端CI](https://github.com/MoYuanCN/Jelee/actions/runs/36759687205)与第3B2 [PR4远端CI](https://github.com/MoYuanCN/Jelee/actions/runs/36769288418)：Windows/Linux foundation和PostgreSQL均通过，完整品牌门禁仍失败。增量品牌、忽略与diff检查通过，完整品牌政策未放宽。

## 后续工作

生产媒体operation尚未注册。当前runner提供程序生命周期控制；文件/网络沙箱在3C独立实现和验收。Linux动态库的固定打包、Windows隔离、tool_versions、媒体规范化/缓存、扫描整合、崩溃残留清理、特殊codec素材与全规模/性能仍待交付。WSL共享NTFS不能保证0700、Windows受限token不能保护新目录DACL时，诊断会安全返回temporary_unavailable。
