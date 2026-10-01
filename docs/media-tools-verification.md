# 第 3B1 段：固定媒体开发工具

源码提交 `3b9be6922cac045070fe435349a835b878de93cc`，基底为第3A段 `29a248908b4915128fdf95b5fa0da8c6d595ce71`。这段交付 Windows/Linux amd64 的可选开发工具；完整第3阶段仍在继续。

## 实际交付

- manifest 在下载前固定 HTTPS 归档来源、完整供应商版本及 SHA256，执行前再固定二进制和许可证哈希。
- 双平台显式 `bootstrap-media`、`media-tools-verify`、`media-toolchain-test` 入口。默认 Go bootstrap 仍只准备 Go；CI foundation 显式准备并验证媒体工具。
- 离线缓存、安装锁、安全解压、原子安装与记录；坏缓存立即清理，已安装但变更的二进制拒绝执行。
- 每次包装器调用检查完整版本、二进制与许可哈希；版本诊断限制10秒和每输出流64 KiB，清除 `FFREPORT` 与 `LD_*`。

## 实际验证

完整记录见[工具证据](evidence/media-tools.txt)。Windows 29 项媒体安全用例和12项既有工具链用例通过；Linux 18 项合并用例通过。两平台离线解压、真实供应商版本、归档/二进制/许可 SHA256 均通过。根代理另执行双平台 Make/PowerShell 的 verify/test 入口，全部退出0。

固定版本分别为 Windows `9.0.2-essentials_build-www.gyan.dev` 和 Linux `n9.0.2-17-g2a571b6068-20260930`。准确来源、源代码修订、归属与许可见 [manifest](../tools/manifest.json) 和[工具许可](THIRD-PARTY-TOOLS.md)。

## 下一分段与限制

3B2 将交付程序生命周期控制、唯读输入 FD 和合成素材。当前生产 probe 保持关闭，现有生产镜像没有 ffmpeg/ffprobe。开发包装器不提供媒体沙箱；Linux 供应商二进制依赖 glibc，不能直接放入当前 scratch 镜像。文件/网络隔离及固定运行依赖在3C独立验证。

完整品牌门禁仍因保留的旧源码失败；没有降低门禁。远端 CI 结果在实际完成后另行回填，不能用本地结果代替。
