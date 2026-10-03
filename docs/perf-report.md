# 性能诊断结果

把管理员与普通用户查询分开，以库 ACL 作为 JOIN 入口，每个授权库先执行有界分页，再合并排序。

| 指标 | 修改前 | 修改后 |
| --- | --- | --- |
| 输入 items | 10,002 | 10,002 |
| 受限用户可见条目 | 1 | 1 |
| 实际访问 items 行数 | 10,002 | 1 |
| 单次 EXPLAIN 执行时间 | 2.683ms | 0.164ms |

查询回归测试直接 EXPLAIN 实际 `listItemsSQL`，并断言访问行数不超过 50，避免使用过期的复制 SQL。日志：[基线](evidence/postgres-baseline.txt)、[修正后](evidence/postgres-optimized.txt)。这证明本样本消除了不可见大库扫描，不证明普遍延迟比例或 P95 目标。

Direct Play 16KiB 微基准为 65,379 ns/op、250.60 MB/s、58,523 B/op、71 allocs/op。它使用本地临时文件与内存响应器，包含测试请求分配；不能替代真实网络吞吐、首字节或播放器 Seek 测试。

只读 NFO adapter 与校验 CLI 已提交并通过 Windows/Linux 测试；尚无 NFO 批量导入/导出或并发写入的性能证据。掃描與圖片已有分項證據，見需求追蹤；完整前端與其餘整合仍待實作。原需求性能场景、CPU 降低、权限开销 ≤10%、大规模内存与 24 小时稳定性均未验收。

## 原媒體串流緩衝重用

`streamWriter.ReadFrom` 透過每個 Handler 的 `sync.Pool` 借用固定 32 KiB 緩衝，複製結束（包含失敗）後清零並歸還。保留逐次 Write 的取消與期限檢查；每個正在複製的串流持有獨立緩衝，GC 可回收閒置 pool 物件。

同一 `BenchmarkOriginalRange`，16 KiB 回應、每次 1 秒、各 3 次：

| 平台 | 修改前 B/op | 修改後 B/op | 修改前／後 allocs/op |
| --- | --- | --- | --- |
| Windows | 58,523 | 25,859–25,874 | 71／70 |
| Linux | 57,782–57,783 | 25,184–25,191 | 64／63 |

每次配置位元組約降低 56%；這是包含測試請求與記憶體回應器的微基準，不代表真實網路吞吐或整體服務效能。背景長測共用主機，因此不以本次耗時差異宣稱速度改善。

證據：[Windows 前](evidence/stream-copy-baseline-windows.txt)、[後](evidence/stream-copy-pooled-windows.txt)、[Linux 前](evidence/stream-copy-baseline-linux.txt)、[後](evidence/stream-copy-pooled-linux.txt)、[Linux race](evidence/stream-copy-race-linux.txt)。Windows 套件測試及 Linux race 通過，涵蓋 Range、取消、權限、並行不同內容，以及讀取錯誤後清除內容。

這只覆蓋 G42.5 的原媒體串流緩衝；編碼器與其他熱路徑配置、整體 heap/pprof 驗收仍待完成。正式 24 小時測試固定在較早 c61c12b007 快照，不包含本次產品修改。
