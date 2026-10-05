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

## 圖片來源複製緩衝重用

來源暫存與來源完整性核對共用固定 32 KiB scratch buffer 的 `sync.Pool`；每次複製持有自己的緩衝，成功或失敗均清零歸還。解碼圖片與快取內容不進 pool；超過來源大小限制、取消及錯誤的既有處理保留。

Windows 的 `BenchmarkImageSourceCopy` 以同一 256 KiB 檔案讀取至 SHA-256，每次 1 秒、各 3 次，修改前 32,838–32,839 B/op、3 allocs/op，修改後 51 B/op、2 allocs/op。此微基準只證明來源複製的配置變化，未量測完整圖片請求或穩態 RSS，也不代表 JPEG 編碼器已重用。

[修改前](evidence/image-copy-before-windows.txt)、[修改後](evidence/image-copy-after-windows.txt)、[Linux race](evidence/image-copy-race-linux.txt)。Windows 圖片套件及 vet 通過，Linux 全套件 race 通過；新增 12 個並行複製使用不同內容與大小，核對各回應完整性。原有來源上限、short write、取消、變更偵測及清理測試保留。

JPEG 標準庫的 encoder 型別未匯出，現有 `jpeg.Encode` 沒有 encoder 重用介面。G42.5 的編碼器與其他熱路徑驗收仍待處理；沒有因此修改需求或宣稱完成。正在執行的 c61c12b007 正式長測不包含本次變更。

## 熱點基準與退化門禁（G26.1、G26.4）

`make bench`（Windows：`scripts/make.ps1 bench`）在下列套件執行全部 `Benchmark*`，輸出到 `.testdata/bench-current.txt`（已被 `.gitignore` 排除）：

| 套件 | 基準 | 量測內容 |
| --- | --- | --- |
| `internal/access` | `Evaluate1k`、`Evaluate10k`、`Compile10k` | 存取規則評估與編譯 |
| `internal/domain/medianame` | `ParsePath` | 10 條固定路徑（電影、季集、動畫絕對集數、日期、特典、CJK） |
| `internal/domain` | `VersionLabels` | 合成探測結果＋檔名的版本標籤判定 |
| `internal/adapter/nfo` | `ReadDocument`、`SourceHashAndParse/*` | 記憶體內 NFO 解析；含讀檔與雜湊的路徑 |
| `internal/adapter/images` | `RenderDecodedSmall`、`ImageSourceCopy` | 640×360 JPEG 解碼縮成 160×90 再編碼（不經快取）；來源複製 |
| `internal/adapter/subtitles` | `DetectCharset/*` | UTF-8、GB18030、Big5、Shift_JIS、EUC-KR 各 48 行 SRT 的編碼偵測 |
| `internal/adapter/http` | `WriteJSONItemPage`、`WriteJSONError` | `writeJSON` 編碼 50 筆列表信封與錯誤信封（假資料、丟棄式 ResponseWriter） |

輸入皆固定且在計時迴圈外準備，全部 `b.ReportAllocs()`；不需要資料庫。讀 100 個檔案的 `ObservedHundredFiles` 受磁碟影響太大，預設以 `BENCH_SKIP` 排除，需要時可手動執行。

可調參數：`BENCH`（基準 regexp，預設 `.`）、`BENCH_SKIP`、`BENCH_COUNT`（預設 6）、`BENCH_TIME`（預設 500ms）、`BENCH_CURRENT`、`BENCH_BASELINE`、`BENCHGATE_FLAGS`；PowerShell 用 `JELEE_BENCH`、`JELEE_BENCH_SKIP`、`JELEE_BENCH_COUNT`、`JELEE_BENCH_TIME` 環境變數。

`make bench-check` 先跑 `bench`，再以 `tools/benchgate` 對比 [`docs/evidence/bench-baseline.txt`](evidence/bench-baseline.txt)。門禁規則：

- 同一基準的多次執行（`-count`）先取中位數，再比較基線與目前的中位數；單次離群值不會單獨造成通過或失敗。
- 基準名稱會加上 `pkg:` 前綴並去掉 `-N`（GOMAXPROCS）尾碼，不同核心數的機器仍能對上。
- ns/op 增加超過 15%（`-ns`）或 allocs/op 增加超過 10%（`-allocs`）即退化；B/op 預設只列出不擋（`-bytes 20` 可啟用）。門檻設負值就停用該項。基線為 0 而目前大於 0 視為無限大增幅。B/op 的絕對增加在 64 位元組以內一律不算退化：分配次數很少的基準會把偶發的執行階段分配攤成幾十位元組，對很小的基線是大比例，但不是退化（例：`BenchmarkImageSourceCopy` 在程式未改動時 48 → 59 B/op，allocs/op 都是 2）。
- 基線有、目前沒有的基準算失敗（`-allow-missing` 可放行），新增但沒有基線的基準只列出不擋；`-match` 可限縮比對範圍。
- 結束碼：0 通過、1 有退化或缺項（列出每一項）、2 用法或輸入錯誤（檔案不存在、沒有任何基準結果、兩邊沒有共同基準）。

例：`make bench-check BENCHGATE_FLAGS='-ns 20 -bytes 25'`；也可以直接 `go run ./tools/benchgate -base A.txt -current B.txt`。

目前提交的基線是在開發機（Ryzen 7 9850X3D、WSL2、go1.27.1、同時有其他負載）上產生，只供參考。同機連跑兩次的 ns/op 中位數差距最多約 11%，已接近 15% 門檻，所以正式基線必須在固定且閒置的 CI 硬體上用 `make bench` 重產並替換，門禁才有判斷力。每個優化提交仍應附 benchstat 前後數據（G26.4）；本工具是回歸門禁，不取代 benchstat 的統計檢定。

CI 不拿這份開發機基線比對：`bench-regression` job 以 `make bench-compare` 在同一台 runner 上交替執行基準提交（PR base 或前一次 push）與目前提交的同一組基準，再用 benchgate 判定（ns/op +25%、allocs/op +10%、B/op +20%）。做法、門檻取捨與反向驗證見 [質量門禁](quality-gates.md#基準回歸同機比較)。
