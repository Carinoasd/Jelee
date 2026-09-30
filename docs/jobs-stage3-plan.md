> 2026-10-01 用户恢复工作。第3A、3B1、3B2、3C1已交付，验证见 jobs-verification.md、media-tools-verification.md、process-verification.md。3C1见probe-verification.md；接着3C2A持久cache、3C2B扫描/API整合，再继续3D。以下保留最初规划，历史描述不代表当前状态。
# 第 3 階段工作範圍：任務、唯讀掃描與媒體探測

規劃日期：2026-10-01，Asia/Taipei。

本文件只整理需求與後續工作，未實作、下載工具、執行 probe、修改 repo 或觸發 CI。依主代理指示，**第 2 階段提交並推送完成後，才開始第 3 階段實作**。閱讀時 repo HEAD 為 `1f9426db96d90a7eb55ca571faf1d0f80523ac4a`，帳號階段仍有工作區內容；這個 SHA 不代表帳號階段最終提交。

需求來源：[原始需求](requirements-source.md)，SHA256 `755b6b32324efe710c3e1135a0c982c45b82f337e90fcb50ab3718a20cba5d07`。以下 G 編號來自原文；交付分段、API 名稱與狀態設計是建議，不冒充原文既定規格。

## 1. 建議先交付什麼

先交付**持久化一次性任務 + 管理員觸發的唯讀媒體庫盤點 + 取消/重啟恢復**。它必須真的遍歷測試目錄、保存進度與盤點結果，具備冪等觸發、容量限制及資料庫競爭測試；不以 sleep 任務或空 handler 代替掃描。

首個增量明確保持媒體 probe、排程觸發、監看、自動清理與 NFO 寫回關閉。盤點記錄屬於候選資料，不把尚未探測的檔案假稱為已完整匯入媒體。這使 G13/G41 的取消、鎖與恢复可以先有獨立證據，也不會因尚未固定的 ffprobe 工具阻擋所有工作。

後續順序：固定媒體工具與安全子行程執行器 → 真實 ffprobe/快取 → 增量掃描整合 → 監看/排程/完整規模測試。各分段只標示覆蓋的子項；整個 G13、G19、G41、G42、G51 不因第一段通過而標為完成。

## 2. 確切需求與驗收對照

| 來源 | 第 3 階段直接相關要求 | 必須保存的證據 |
| --- | --- | --- |
| G13.1 | cron/interval/一次性，任務定義持久化，啟停、手動觸發、歷史/日誌 | 第一段只交付一次性與歷史；排程另段驗收 |
| G13.2 | PostgreSQL advisory lock 或租約，防同任務重疊；實例 leader | 兩個 worker/兩個服務同時觸發、領取與恢復的真實 PG 測試；排程 leader 不以單機測試替代 |
| G13.3 | 取消、恢復、進度/處理數/總數/ETA、失敗原因及重試 | 取消後終止、重啟續跑、重試上限與錯誤脫敏；尚未知總數時顯示未知，不編造百分比/ETA |
| G13.4 | fsnotify 去抖；路徑/size/mtime/快速指紋增量；不重探測未變檔案；批次/檢查點；刪除確認閾值 | 1000 檔案初掃/重掃/少量修改的實際計數；中斷與目錄失聯不被當成整庫刪除 |
| G13.5 | 目錄並行、I/O/CPU 分離、低優先完整校驗、掃描窗口，NFO/圖片參與增量 | 分離併發額度及公平性測試；未交付的圖片解析/排程窗口另列 |
| G13.6 | 其他刷新、寫回、圖片、清理、統計、一致性任務 | 共用任務介面可接入；未实现的業務不註冊假 handler |
| G19.1–G19.2 | ffprobe JSON 為主，MediaInfo 補充、Matroska 工具；規範化視訊/音訊/字幕/時長/容器/章節 | 真工具版本、黃金 JSON、實際合成媒體；DV/HDR/TrueHD/ATMOS/PGS 缺樣本要逐項列未驗證 |
| G19.3 | 快取鍵含路徑、size、mtime、工具版本及必要指紋；按庫/條目失效及重建/清理 | 不變命中、只改 mtime/size/內容/工具版本失效、失效與並發探測不重複提交 |
| G19.4 | 損壞檔案標記 `probe_failed`，可重試，不停止整次掃描，錯誤脫敏 | 混合正常/損壞檔案仍完成，其計數與單項失敗原因可查 |
| G19.5 | 有界探測並發/進程池、批次寫入、防重複探測 | 子行程峰值、DB 語句/批次數、命中率及耗時 |
| G09.1–G09.6 | 本地卷、參數陣列執行、工具版本表、超時/取消/進程組/輸出上限、隔離暫存、根邊界與低權限 | `tool_versions`、storage-layout、惡意檔名/符號連結/假工具/超量輸出/子孫進程取消與清理 |
| G22.1–G22.5 | `.jeleeignore` 語法；舊規則先查精確上游；繼承/就近優先、編碼、符號連結不穿越、快取與命中來源 | 來源版本及契約、變更規則後重掃；不能把未審核的舊檔名一概宣稱相容 |
| G41.1–G41.6 | 所有非同步工作共用隊列/worker；兩級優先、公平、有界背壓；CPU/I/O/總額度；DB 池預算、多實例鎖/心跳 | 隊列滿載時明確拒絕；並行數、兩級不飢餓、連線保留量、租約失效後舊 worker 無法提交 |
| G41.7 | 負載自適應是可選项，恢復有滯後 | 第一段可使用固定且可配置額度，不必先加入自適應 |
| G41.8–G41.10 | 深度、在跑/等待/耗时/取消/失敗/Go/DB 指標；race 混合壓測；硬體調參表與平穩曲線 | 初段記錄自身任務指標，完整混合負載與吞吐另做，不拿單測代替 |
| G29.2–G29.5 | 有界 errgroup/worker，context、無忙等/洩漏，進程樹回收、崩潰暫存清理、race | 模擬阻塞、取消/timeout、worker crash、優雅關閉，FD/進程/goroutine 恢復 |
| G42.1、G42.3–G42.4、G42.7 | 禁止整檔讀入；流式目錄/有界批次；有界快取；stdout/stderr 限流截斷，大輸出走暫存 | 最寬目錄與1萬/10萬/50萬項分級曲線；不能用一次 WalkDir 將巨型目錄排序入記憶體便聲稱有界 |
| G42.8–G42.10 | 容器記憶體/Go 指標、pprof、24 小時穩態與大規模驗收 | 第一段短測與完整長測分開列結果 |
| G04.2–G04.5、G36.1–G36.3、G36.5 | 不改已發布 migration；參數 SQL、約束/索引/短交易/批次；jobs/job_runs/job_locks/probe_cache 及資料保留 | 新 migration up/down/up、EXPLAIN、競爭/回滾；實際表名以設計落盤為準 |
| G08.4、G08.7、G25.5 | 掃描觸發 Idempotency-Key；大小限制/超時/取消到 DB 和子進程 | 重試觸發只得一個 run；斷線與明確取消的語意可驗證 |
| G28.2、G28.4–G28.6 | 層次、fx 啟停/停 worker/終止子行程；scan CLI；每段開關/契約/回滾 | architecture、啟停/停用開關、CLI/API 一致用例 |
| G39.6、G39.12、G39.14 | NFO 優先/欄位鎖；損壞保持原文并標記；批次走任務 | 先接只讀解析與 `nfo_invalid`；原子寫回/備份未完成時不可啟用寫回任務 |
| G46.2–G46.6 | jobs/scan/probe 組件、taskId/jobRunId/trace、任務進度、絕對路徑與敏感資料脫敏 | 日誌/任務 API/CLI/錯誤中的敏感樣例零命中 |
| G50.1、G50.3–G50.5 | doctor 工具/路徑、孤兒與快取一致性、dry-run 修復、啟動不變量 | 缺工具/失聯根/權限/磁碟故障；第一段只診斷/報告，不自動清除資料 |

另有跨段必守項：G10.1/G10.3/G10.10 的禁轉碼与原媒體不變；G03 四語；G07/G48 的管理權限與库 ACL；G11 的外連/隱私；G30/G38 的品質與 CI 門禁。原文「五、性能验收」另指定 **100 個檔案增量掃描**基準；不能和 G13 的 **1000 檔案**驗收混為一項。

## 3. ffprobe、停用與 fallback 的規則

1. **允許的執行期探測**：G19.1 明定 ffprobe JSON 主探測；G51.7 明定執行期工具包括 ffprobe、可選 mkvtoolnix、mediainfo。G37.1 要求最終生產映像包含固定可追蹤的探測工具。
2. **ffmpeg 留在開發/測試**：G37.1、G51.7 禁止生產映像與生產呼叫鏈依賴 ffmpeg。G51.8 可用 `.tools/` 中固定 ffmpeg 生成合成 fixture；產物進 `.testfixtures/`，不入 Git。不能因本機 PATH 恰好有 ffmpeg 而自動開啟能力。
3. **缺 ffprobe 時相關能力停用**：依 G09.3 回報可操作的缺工具/版本錯誤，工具狀態和 probe capability 清楚可查。不得把探測成功、空 JSON、猜測 codec 當 fallback；不得呼叫 ffmpeg 代跑 probe；不得啟動舊 .NET 旁路或轉碼救場。已存在的原檔直投不因純探測工具缺失被默默改變。
4. **損壞檔案與缺工具不同**：檔案損壞用 G19.4 `probe_failed`；工具不可用用單獨安全能力錯誤。整批不可把所有檔案誤記為損壞。重試明確且有上限。
5. **MediaInfo 是補充**：G19.1 沒有授權任何未驗證的自動降級算法。第一段只開 ffprobe 時，MediaInfo/mkvtoolnix 的補充欄位標為尚未實作；若日後提供 fallback，須固定工具、來源優先與欄位 provenance，經契約測試才開啟。
6. **工具讀取也要限制網路**：不能只排除使用者傳入 URL；媒體/播放清單可能引用其他檔案或遠端位置。runner 要限制可用協定、輸入類型與子進程網路/檔案能力，確認 ffprobe 對被測格式不會跨根讀取或外連。確切旗標要從選定版本官方文件/`-help` 核對，現在不臆測命令參數。
7. **不重開原始路徑競態**：現有 `os.Root` 能約束 Go 開檔，但直接將重新組合的字串交給子進程會重新解析路徑。Linux 傳入安全已開啟 FD、Windows 安全 handle/受控只讀 staging 等方案必須單獨設計與驗證；沒解決前 probe 保持關閉。原始大媒體不能因此整檔塞入 RAM。
8. **mkvpropedit 的寫入能力不能碰原媒體**：G19.1 工具列表不推翻全局資產保护與 G10.10。第一段僅需要只讀資訊；章節/附件寫操作不因工具已安装而註冊。
9. **測試缺工具的處理**：G51.10 要明確 skip 並列原因；完整工具鏈 CI 必須 fail closed，不能把 skip 當通過。探測專項可提議 `JELEE_REQUIRE_MEDIA_TOOLS=true`，但這是待實作名稱，不是現有設定。
10. **兩種其他 fallback 不要混淆**：G04.8 禁 SQLite 生產回退；G51.9 要求的無 Docker fallback 是本地固定版本 PostgreSQL 測試執行方式，仍未實作。G39.12 的 NFO 損壞回退是其他 metadata 來源，不是轉碼或 ffmpeg。

原文沒有指定名為 `probe_fallback_disabled` 的旗標或錯誤碼。建議 probe 預設關閉、顯式啟用並檢查能力；真正旗標/錯誤碼應在設計提交定案並補四語/OpenAPI。G51.6 對 ffmpeg/ffprobe 使用括號合述「仅测试素材生成与开发调试」，但 G19.1/G37.1/G51.7 對 **ffprobe 執行期探測**有更明確的規定；本計畫採明確規定，禁止擴張成 ffmpeg 生產可用。

## 4. 工具與依賴的現在狀態

已讀 [manifest](../tools/manifest.json)、[工具文件](../docs/toolchain.md)、[需求解释](../docs/requirements-clarifications.md)。

- `tools/manifest.json` 是 G51.3 允許的等價清單。目前 `tools` 只有 Go 1.27.1；另有固定 Go build image、已有 Docker 29.7.2、PostgreSQL 16.15 test image 紀錄。**沒有 ffprobe、ffmpeg、mkvtoolnix、mediainfo、fsnotify 安裝實作**。
- ffprobe 等二進位必須先加入版本、HTTPS 來源、每平台 SHA256、安裝/解壓路径、是否必需、许可证与归属，再下載。不可先下載後補 manifest。Linux/Windows 優先（G51.13）；ARM 或 macOS 不可填虛構 hash 假裝支持。
- 現有 bootstrap/verify 實作主要針對 Go，不能以 manifest 多一筆就宣稱能安裝媒體工具。需擴充 Windows/POSIX installer、wrapper、installed record、version/hash verify、安全解壓、離線與失敗重試測試。
- 所有下載/解壓/快取在 `.tools/`，wrapper `.bin/`；Go 依賴與快取走現有 scripts/run-go.ps1 或 `.bin/go`。禁止系統套件管理器、全域 GOPATH/npm/PATH/shell profile 改動（G30.6、G51.1–G51.5、G51.12）。
- 新 Go 套件固定在 go.mod/go.sum；現在已有 `golang.org/x/sync v0.23.0`、`x/sys v0.48.0` 可評估 errgroup/平台功能，不能直接假設其 API/實作滿足進程樹取消。fsnotify 目前沒有依賴，放在監看段再查官方版本與加入；不先裝 cron/fsnotify 滿足第一段一次性任務。
- FFmpeg 專案來源與實際 Windows/Linux 分發包可能不同；後續要核對二進位供應者、build config、動態函式庫、LGPL/GPL 等實際授權，不用專案名稱猜许可证。生產只帶允許的探測可執行檔及必要函式庫/聲明，不複製整個含 ffmpeg 的 bundle。
- [現有 Dockerfile](../Dockerfile) 的 scratch 映像沒有媒體工具。加入動態 ffprobe 前須盤點 interpreter/shared libraries；是否換最小基底需 ADR 與映像內容檢查，不能直接 COPY 後宣稱可執行。
- G51.8/14/15 所需 gen-fixtures、完整工具鏈 CI、干净环境重建尚未完成。現有 test-integration 要求專用 `jelee_test` 與 `JELEE_REQUIRE_INTEGRATION=true`；不能對使用者 DB 做測試 migration/down。

## 5. 目前可重用的程式邊界

| 已有位置 | 能重用的責任 | 需要補上的部分 |
| --- | --- | --- |
| `internal/domain`、`internal/app`、architecture test | 純領域模型、介面端口、依賴方向 | Job/Run/Scan/Probe 型別、狀態不變量、repository/filesystem/probe/clock 端口 |
| `internal/adapter/postgres/migrations/000001_catalog*` | libraries、library_roots、items、media_sources，root+relative_path 唯一 | jobs/runs/鎖與租約、盤點 generation/checkpoint、probe_cache、tool_versions、必要 stream/章節欄位 |
| phase2 AccountRepository/Actor/live session 授權 | 管理員/会话再驗證、庫 ACL、審計 | 觸發/查看/取消/重試 job 的真實 DB 授權，不能信任请求中的 actor/root 路徑 |
| `internal/adapter/http/strict_json.go`、login limiter 模式 | 嚴格 JSON、有界容量、集中錯誤與四語 | job 類型白名單、嚴格分頁、Idempotency-Key、滿载與probe不可用合同 |
| `internal/adapter/media/direct.go`、`open_*.go` | 原檔只讀、安全根、普通檔案驗證；現有不執行外部程序 | 流式目錄枚舉、根切換/權限錯誤分類、child process 安全輸入橋接 |
| `internal/adapter/nfo` | 有界只讀解析、靜態錯誤、原文保存、無出站連線 | 每庫 read-only/off、metadata merge/locks、增量 fingerprint 和保存解析結果；不包含寫回 |
| `internal/platform/runtime/runtime.go` | fx service、PG pool、HTTP lifecycle | worker 的 OnStart/OnStop、停止領取→取消/等待子行程→保存/釋放run→關DB的明確順序 |
| `cmd/jelee-cli` | doctor、import-video、離線 NFO 與帳號命令 | scan/job status/cancel/retry 命令；不接受任意 command 或外部工具旗標 |

現有 catalog schema 尚無 jobs/job_runs/probe_cache/tool_versions；目前沒有可重用的 Go 工作佇列/掃描/探測執行器。下一 migration 號在 phase2 推送後重新確認，通常接續 000002，**不可改寫已發布 000001/000002**。

## 6. 可獨立驗證的交付分段

### 3A：持久化一次性唯讀盤點（首個可交付）

**涵蓋子集**：G13.1–G13.4、G41.1–G41.6、G29.2–G29.3、G42.3、G08.4、G36.1/2/5、G46.3–G46.5。

建議落盤：`internal/domain/jobs.go`、`internal/app/jobs.go`/`scan.go`、`internal/adapter/postgres/jobs*.go` 與新 migration、`internal/adapter/media/scan*.go`、`internal/platform/jobs/`、HTTP/CLI/config/runtime 裝配與 docs。名稱在首個設計提交定案。

- 只接受註冊的 `inventory_scan` 類型和資料庫 library/root ID。root 來自已保存且授權的設定；外部請求不能任意指定絕對路徑、程式或參數。
- HTTP 建議：`POST /api/v1/libraries/{id}/scan`（Idempotency-Key、202）、分頁 job list/detail、明確 cancel/retry 動作；CLI 呼叫相同 app 用例。先限管理員，repo 在交易內再驗證 live session/授權。
- PG 保存定義、run、owner/lease/checkpoint、attempt、not-before、requester、priority、計數與安全 error code。以唯一約束/短交易防重複領取；同庫活躍掃描明確拒絕或返回既有 run，不靜默另排重複工作。
- 狀態規劃：queued → running → succeeded/failed；持久 cancel_requested → cancelled；lease 過期的未完成 run 可按重試策略恢復。復原是至少一次、結果冪等；不能聲稱 exactly once。舊 owner 的 checkpoint/result 必須由 owner/version fencing 拒絕。
- 每段更新是短 PG 交易；不能持有資料庫交易去遍歷目錄或等待 probe。租約續約失敗後及時停止工作，避免新/舊 worker 同時提交。若採 session advisory lock，其佔用連線必須計入 pool 預算。
- queue/pending rows 與記憶體 channel 都有限額；歷史保留亦有限。使用兩級 priority 加有限配額輪轉，既照顧手動工作，也不讓背景工作永久飢餓。directory I/O、probe CPU、總工作額度分開設定，保留 HTTP/DB 連線容量。
- 用分批 ReadDir 等方式串流列舉，對單一巨型目錄也有界。durable directory frontier/已完成批次允許重啟；未完成目錄可重新讀取並以唯一鍵去重，不把跨重啟的 OS directory offset 當穩定識別。
- 首段只保存檔案候選與 change/missing 統計。取消、權限拒絕、根失聯、不完整遍歷不能標記刪除；完整 generation 后才產生待確認差異。大比例/大數量缺失觸發保護，完全不刪原媒體/NFO/圖片，也先不自動刪 catalog。
- HTTP request context 只控制提交/查詢交易；已成功入庫的背景任務有獨立、可控的服務生命週期。使用者要取消已接受的任務，走持久 cancel 動作；不能把 HTTP 斷線誤作已入庫任務消失。

**最小驗收**：真 PG up/down/up；同 key 重試唯一 run；兩 worker 爭同庫不重疊；queue滿返回明確錯誤而非無界等待；1000 個自建小檔盤點計數準確；取消停止、kill/restart 從檢查點恢復且不重複結果；根失聯/拒絕/大比例缺失不清庫；原文件 hash 不變；Windows/Linux race 與無 goroutine/連線殘留。不能把「未啟用 probe，probe 次數為零」當成已完成 G19 快取驗收。

### 3B：固定工具、安全執行器與 probe 能力診斷

**涵蓋子集**：G09.2–G09.6、G29.4、G42.7、G51.1–G51.8/10–15、G37.1、G50.1/5。

先核對來源/许可证/架構，manifest先行，再擴充雙平台 installer/verify/fixtures。runner 僅接受受控 operation，使用固定 binary 絕對路徑與參數陣列，無 shell；讀入/輸出/工作目錄/環境/時限/並發均有界。

Windows 需要可終止子孫進程的策略（例如經查證的 Job Object 使用方式）；Linux 需要 process group 管理。單純 exec.CommandContext 殺直接 child 不可冒充整個進程樹已回收。stdout/stderr 封頂並持續正確 drain/取消；超出上限終止并分類；任何大輸出暫存置專用生成目錄，startup僅清理本系統標記且符合保留規則的殘留。

**驗收**：缺工具/錯版本/錯 checksum 拒絕；惡意 argv/檔名/穿越/符號連結替換、FD/handle安全、外連嘗試拒絕；helper 子孫進程遇 timeout/cancel 確認退出；無 shell/ffmpeg生產呼叫；兩平台包含空格/Unicode 路徑的成功真probe；生成素材與來源hash保存，鏡像非root且沒有ffmpeg。

### 3C：真實 ffprobe、增量/快取與只讀 NFO 整合

**涵蓋子集**：G19.1–G19.5、G13.4/5、G10.6、G39.6/12、G42.4。

規範化有界 ffprobe JSON；尺寸/時長/幀率等數值拒絕溢位、不合法與非有限值。快取以 root+relative path+size+高精度mtime+tool/version+fingerprint識別；並發同檔去重，probe前後stat/fingerprint不一致則丟棄/重試，避免把變動檔案的混合結果入庫。快取與來源/條目寫入用短交易，cache大小/保留/清理明確。

盤點變化才排probe；未變media而NFO改動，僅重讀NFO。NFO保持read-only/off且尊重鎖，損壞標記不覆寫；圖片此段只識別存在與mtime，不宣稱圖像處理已完成。檔案/工具失敗逐项記錄，不誤刪，也不讓一個損壞檔停止全庫。

**驗收**：真工具1000檔初掃→不變重掃probe新增為0→修改K檔只probe K；工具升版/fingerprint失效；損壞檔/變動檔/多副檔名；黃金資料與實際probe結果對照；快取命中率與耗時報告；原媒體/NFO hash未變；取消子進程與job狀態一致。

### 3D：忽略規則、監看、排程與較完整規模

**涵蓋**：G22、剩餘 G13.1/2/4/5、G41.8–10、G42.3/8–10；完整 G13.6 其他業務仍分別交付。

先完成 `.jeleeignore` 與已查證舊 `.ignore` 的合併合同，才開持續監看；固定 fsnotify 版本，測事件風暴/去抖/overflow→安全全掃、多平台差異。cron/interval 使用持久時區與leadership/租約，清楚定義重啟missed-run政策。最後做不同核數/記憶體基準、1萬/10萬/50萬檔曲線與24h穩態。資料不足時只報已跑規模。

## 7. 還需要確認的來源與架構資訊

这些是實作前調研項，可從 repo/官方文件/本地工具自行完成；目前不需要對使用者提出批准問題。

1. phase2 最終 commit、migration schema version、live session/admin/ACL API 介面，確定階段3的開始基線。
2. ffprobe/FFmpeg、MediaInfo、MKVToolNix 所選精確版本的官方 CLI/JSON schema、Windows/Linux分發來源、SHA256、build flags、授權與動態依賴。現在沒有新工具版本/hash，不能填猜測值。
3. Windows Job Object/handle與Linux process group/fd的官方API與當前Go/xsys支持；安全傳遞被root約束的檔案給ffprobe之方案與效能成本。
4. `.ignore` 精確來源已在本地 `DotIgnoreIgnoreRule.cs`（精确来源见 requirements-clarifications.md） 看見：最近祖先、空檔/沒有有效規則即全忽略、Windows斜線規範化等；仍需固定至上游審計commit `52a680c578f1af888ebb74cefcb89b736f9c5738` 的程式與測試建立契約。其他舊專屬忽略檔名尚無已確認入口，沿用 [requirements-clarifications](../docs/requirements-clarifications.md) 的保守界線。
5. 本地/網路FS檔案身分、mtime精度、rename和symlink行為；Windows/WSL DrvFS與原生Linux測試分开。網路FS掛起的open/stat不保證硬取消，需部署說明和故障注入，不能宣稱無條件即刻取消。
6. queue/worker/global/CPU/I/O/DB保留/輸出/暫存/快取/重試/刪除保護的具體預設；先寫配置矩陣再寫worker，避免跨limiter鎖次序死鎖。
7. 合成fixture能覆蓋的格式與特殊codec能力。DV/Atmos等不能用普通短片代替；沒有合法可用來源就標為缺口。

## 8. 提交、證據與回滾

- 每一可編譯分段使用獨立 Conventional Commit，先測試再提交；不改 phase2 已推送歷史。具體順序須記錄是對原文完整階段路線的增量切分，不宣稱已完成完整替換。
- 需求矩陣更新到確切G子項、檔案、測試與實際commit；`docs/storage-layout.md`、`docs/jobs.md`/`docs/scanning.md`、工具/部署/權限/故障說明按實際交付新增。
- 每段新增開關預設關閉；關閉時停止新任務，按明確drain/cancel政策退出worker，保留可恢復run。程式回滾以停用能力為首選；migration down僅對專用測試DB做驗收，生產回滾需备份/前向相容策略。
- 独立記錄 unit、真PG integration、Windows/Linux race、子进程、fixture、容器、遠端CI、效能與跳過原因。完整品牌 gate 目前的遺留失敗不能透過擴大allowlist掩蓋。
- 首段完成判定只對 3A 清單；stage3整體、G00–G51整體完成仍以原需求全部證據為準。
