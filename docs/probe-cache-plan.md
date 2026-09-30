# 3C2：媒體探測快取與增量處理方案

狀態：**root 已選定本方案四項方向；migration、repository、worker、API 與以下驗收仍未完成。** 本文件不屬於 3C1 的實作證據。採新增 `000004_probe_cache.up.sql/.down.sql`，不改已發布的 000001–000003 migration。

交付分為兩個 PR：3C2A 先做 cache／domain／PG 契約與 migration、租約／quota 的真實 PG 驗證；3C2B 再做 worker／API 串接與 1,000→0→K 真實媒體驗收。3C1 凍結期間，後續程式／SQL 草稿只置於 workspace `work/probe-cache/`，不混入目前 Go tree 或 3C1 快照。

## 1. 需求與現有界線

來源為 [原始需求](requirements-source.md) G19.3、G19.4、G19.5、G13.4。

| 需求 | 本階段擬交付 | 仍不包含 |
| --- | --- | --- |
| G19.3 | 依檔案版本與工具身份查快取；按庫／條目失效、重建；有界清理 | 長期保存每個歷史媒體版本 |
| G19.4 | 媒體失敗逐項記錄，其他檔案繼續；固定錯誤碼、明確重試 | 將 helper／沙箱故障誤標為損壞媒體 |
| G19.5 | 有界 worker 與跨實例檔案租約；批次查詢、提交與去重 | 常駐可重用 ffprobe 子程序池；目前仍每次啟動受隔離的程序 |
| G13.4 | 掃描後依 path／size／mtime／edge fingerprint 跳過昂貴探測；可恢復 checkpoint | fsnotify／去抖、NFO／圖片增量解析、完整檔案內容證明 |

已核對的現況：

- [jobs ports](../internal/app/jobs_ports.go) 分開公共管理與 worker 租約操作；公共操作交易內重新驗證 session／admin。
- [schema 3](../internal/adapter/postgres/migrations/000003_jobs.up.sql) 的 `jobs.kind` 只接受 `inventory_scan`，同庫 queued／running 唯一；`job_inventory` 是每個 job 的相對路徑、大小、mtime 觀察。history 清理會 cascade 刪除 inventory。
- `library_inventory_baseline` 只有路徑，不能充當 metadata cache；它的 missing／review 保護維持原有語義。
- [worker](../internal/platform/jobs/runner.go) 在目錄階段結束後直接 Finish；[PG](../internal/adapter/postgres/jobs_execution.go) 的每次保存、續租、結束都依 DB 時鐘及 owner／generation 檢查有效租約。未完成目錄會重掃；probe phase 必須在全部目錄 done 後開始。
- [Adapter](../internal/adapter/probe/adapter.go) 已有隔離 `Probe`、來源 stat／edge／重新開檔驗證及空結果錯誤規則；**沒有只計算快速指紋而不執行 ffprobe 的公開接口**。因此 `Inspect` 是本階段必要新增接口，不能以呼叫 `Probe` 後再宣布 cache hit 代替。

## 2. 建議工作流程

保留 `inventory_scan` kind。由伺服器明確啟用 probing 時，在同一 job 內加入第二階段：

1. 原有 scanner 完成所有目錄並持久化 inventory。此階段不執行媒體工具。
2. `BeginProbePhase` 以短交易驗證 parent job lease、沒有 pending directory、取消旗標，建立或恢復 phase checkpoint。工具／parser 身份及 policy 在 phase 建立時固定。
3. 以 inventory UUID keyset page 讀取最多 32 個 `kind=video` 的候選項。root 絕對路徑只由 DB 私有查詢解析，不從 HTTP 或快取回應接收。
4. 交易外安全 `Inspect`：唯讀開檔、大小／mtime／最多 128 KiB edge SHA；比對 inventory 的 size／mtime。掃描後已變更的檔案記為 `source_changed`，等待下一次掃描，不拿舊 inventory 配新內容。
5. 批次查 cache。完整版本鍵、失效 generation 與 TTL 都符合才可 hit；否則嘗試取得該路徑的唯一 probe lease。
6. hit 不執行程序；miss 在交易外執行既有隔離 Adapter。回傳 Observation 必須與已預留的 stamp、工具身份一致。提交前再以有界 Inspect 核對目前來源；變更即丟棄。這仍是觀察一致性檢查，不是原子檔案 snapshot。
7. `CommitProbeBatch` 原子更新 cache、釋放 lease、更新 quota 與連續 checkpoint／counter。成功、負快取及 hit 都只能經此交易計入一次；不能先推進 cursor 再保存結果。
8. cursor 到結尾且無持有中的 child lease，phase 才完成。擴充 `FinishJob` 檢查：已啟用的 probe phase 未完成時不得成功結束。原 inventory 的 missing／baseline 規則不變。

初版每個 job 保持單一協調者，以現有多 job worker 並行不同庫；程序 runner 每實例仍限 2。跨實例另外限全域 probe leases，不能將每實例上限誤稱叢集上限。對一個 page 可批次讀取／提交 hit；實際 miss 可逐個提交，也可將已完成且租約仍有效的最多 16 個結果一起提交。**只允許 checkpoint 後連續的 prefix；後面先完成的結果不得跳過前面尚未完成項。** 初版不需要為每個 inventory 項目建立另一份永久 queue／結果 ledger。

暫停或程序崩潰可重試尚未提交的 prefix；已提交 cursor 前的項目不重做。一次 job 內尚未提交的 probe 最壞可重做 16 項，不能承諾程序的 exactly-once 執行。若採逐 miss 提交，重做上限降為正在執行的項目。

## 3. 身份鍵與來源版本

### 路徑身份

`(library_id, root_id, relative_path)`；資料庫以 `(root_id, relative_path)` 唯一，並用現有 `library_roots(id, library_id)` 複合 FK 保證歸庫一致。

`relative_path` 為原有 slash canonical、UTF-8、無控制字元、1–1,024 bytes、深度不超過 128 的文字。不能 lower-case、以顯示名稱做鍵、保存絕對 root，或按 basename 去重。路徑比較沿用 scanner 的精確文字規則；Windows 與大小寫敏感檔案系統間的別名／重複 root 問題不靠快取推斷解決。

cache 不以 inventory ID 為永久鍵，也不以 inode 單獨去重。相同 inode 的不同 hardlink 路徑仍是不同 entry；不同庫的 cache 不互相暴露。

### 完整有效性鍵

```text
root_id + relative_path
+ size + modified_unix_nano + fingerprint_version + edge_sha256
+ tool_versions.identity_digest
+ library_cache_generation + root_generation
+ optional catalog_item_id + item_probe_generation
```

SHA256 以 32-byte `bytea` 保存；邊緣演算法沿用 `edge-sha256-v1`。mtime 保存 signed int64 nanoseconds，不做 SQL timestamp 精度截斷。inode／file ID 僅在安全開檔期間用來防替換，不作跨重啟穩定鍵。

`tool_versions.identity_digest` 是可信工廠由有版本、長度明確的 canonical 欄位計算的 SHA256，包含：

- ffprobe 精確 vendor／upstream version、執行檔 SHA256；
- runtime library bundle：固定 loader 與每個共享庫的 path／SHA256，排序後的 manifest 身份；這裡的 lib 指程序依賴，與媒體庫 generation 分開；
- parser identity，例如 `media-metadata-v1`，以及 normalized JSON schema version；
- 固定探測 argv／demuxer／protocol 允許清單版本、sandbox policy version；
- fingerprint algorithm version。

不能只用 `ffprobe 9.0.2`，也不能從 mutable manifest、HTTP、媒體 JSON 或 DB 字串反向授權執行檔。DB 的 tool_versions 是身份記錄；是否可執行仍由 embedded pins、sandbox 與 runtime 工廠決定。Observation.ToolIdentity 必須等於該 phase 的預期身份，不可讓 worker 任意覆寫。

同 inode 未取樣中段改寫且還原 mtime 的情況仍可能 hit。詳細限制見 [媒體 metadata](media-metadata.md)。未來低優先級完整 checksum 是另一版本策略，不能宣稱本階段已提供。

## 4. Migration 004 建議結構

下列為 schema 契約草案，不是已存在的 SQL。

### `tool_versions`

- `id uuid PK`、`identity_digest bytea UNIQUE CHECK length=32`；不可 UPDATE 身份欄位。
- 有界 ffprobe vendor／upstream version、exe SHA256、runtime bundle SHA256、parser／schema／sandbox／arguments／fingerprint version。
- `created_at`。只保存白名單身份，不保存本機工具絕對路徑、stdout、stderr 或原始 JSON。
- 由可信 runtime 註冊；相同 digest 重放必須逐欄一致。外鍵 referenced 時不得刪除。

### `probe_cache`

- PK `(root_id, relative_path)`；`library_id` 與複合 root FK；可選 `item_id`。
- 上述 file stamp、tool version FK、library／root／item generation。
- `state`：`pending | ready | failed`。只保留這個路徑目前的一份版本；新版本取代舊版本，不累積歷史。
- `metadata jsonb NULL`，只有 ready 非 NULL；failed／pending 必須 NULL。metadata 是白名單 domain 模型的重新序列化，從不直接保存 ffprobe JSON。
- `error_code` 固定 enum，ready／pending 空值；`expires_at`、`retry_after`、`updated_at`、`last_used_at`、`failure_count`（有界 0–10）。
- `lease_owner`、`lease_generation bigint`、`lease_until`、`lease_job_id`、`lease_job_generation`。全有或全無；每次取得租約從新增的 `probe_lease_fence_seq AS bigint NO CYCLE` 分配 generation，溢位拒絕。**不能讓刪除後重建的 row 從 generation 1 重來**，否則舊 worker 可能遇到 ABA 而提交。parent job FK 刪除採 `RESTRICT`：正常 Finish／Release 必須先清掉引用，避免 history trim 靜默刪掉仍在工作的 lease。
- `charge_bytes bigint` 記錄受管理的 payload／row allowance；metadata 的 `octet_length(metadata::text)` 有硬上限。pending leased row 預留最大結果額度，避免工具完成後才發現容量不足。
- 索引：`(library_id, updated_at, root_id, relative_path)`、terminal `(expires_at, root_id, relative_path)`、active `(lease_until)`、淘汰用 `(last_used_at, root_id, relative_path)`，以及 `(item_id)`、`(tool_version_id)`。phase 的 tool version FK 同樣索引，避免刪除未引用工具身份時全表掃描。

`pending` 不是永久失敗紀錄。runtime 故障只釋放 lease、清 metadata，不能將該 row 改成 `failed/probe_failed`。

### `probe_job_state`

- `job_id PK REFERENCES jobs ON DELETE CASCADE`，`phase=waiting|running|done|runtime_unavailable`。
- 固定 `tool_version_id`、probe policy snapshot、`scope=incremental|library_rebuild|item_rebuild`、可選 `target_item_id`。
- `cursor_inventory_id`（UUID，nullable）、`processed/hits/probed/failed/changed/unavailable` counters、safe `error_code`。
- 必須先確認所有目錄 done 才固定 inventory UUID 順序；phase 開始後不允許重新 SaveScanBatch／清除已完成 inventory。正常租約恢復仍從這個 cursor 開始。
- counters 不超過 job max_entries；一個提交的 prefix 只能累加一次。舊 cursor 的提交可識別為已提交同一 prefix，或安全拒絕，不能再次加計。
- 公共 job summary 的 inventory files／bytes 語義不變；probe 進度另提供明確欄位或子資源。

`processed` 計已 checkpoint 的不同 inventory 項；hit、negative_hit、探測成功、媒體失敗、changed、input unavailable 是互斥結果，總和應等於 processed。`probed` 可另作已提交的探測結果數，但不能代替實際程序啟動 telemetry：崩潰後重試或 runtime 失敗可能啟動程序卻沒有提交結果。job 成功表示掃描與 phase 已跑完；若 failed>0，公開 summary 必須顯示單項失敗數，不宣稱全部媒體探測成功。

### Scope generations 與 quota

- 004 在 `libraries` 加 `probe_generation bigint DEFAULT 1`，在 `library_roots` 加 `probe_generation bigint DEFAULT 1`，在 `items` 加 `probe_generation bigint DEFAULT 1`。只增加欄位，不重寫舊 migration。root path 修改需要同交易遞增 root generation；建議 DB trigger 保護日後新增 root-edit API。
- library／item 失效只遞增單行 generation 即可即時生效，不需要同步 UPDATE 十萬條 cache。每次命中／提交都 join 當前 generation。尚未映射 catalog item 的 inventory 不捏造 item ID；建立／改變／刪除 media_sources 映射必須同交易遞增受影響 item generation（建議 004 trigger），避免 item-scope 的候選集合在 cursor 前悄悄改變。
- `probe_cache_quota` 一個全域 singleton，及 `probe_library_quota` 每個啟用 probing 庫一行，保存 row／charged bytes／active leases 計數與上限。quota scope rows 本身亦有全域上限。
- 全部 row 插入、替換、刪除、租約預留／释放，必須在同一交易維護精確差額；加減用安全溢位檢查。不可每次提交全表 `COUNT`／`SUM`。cache 對 library／root／item 的 FK 刪除採 RESTRICT，由未來明確的刪除流程先有界清 cache／quota；不得讓未計帳的 cascade 使 counter 失真。

新二進位 Ready 要求 schema 4；舊二進位 Ready 維持只接受 schema 3，不允許混用。004 down 先要求 probe workers 已停，刪除上述 cache／phase／identity／quota 表及新增 generation 欄位；保留 schema3 inventory、jobs、baseline、catalog、使用者和媒體原檔。004 down 會丟失 cache／probe 進度，需明確記錄；原 inventory 不會自動回退或刪除。

## 5. 有界性建議值（待確認）

| 項目 | 建議預設 | 允許範圍／硬上限 |
| --- | --- | --- |
| 全域 cache rows | 100,000 | 1,000–1,000,000 |
| 每庫 cache rows | 50,000 | 1,000–500,000，且不超全域 |
| 全域 charged bytes | 1 GiB | 16 MiB–16 GiB |
| 每庫 charged bytes | 256 MiB | 16 MiB–全域上限 |
| 每份 normalized metadata | 128 KiB | 固定硬上限 |
| 每 row 固定預留 allowance | 2 KiB | 另加 metadata bytes；active lease 另預留 128 KiB |
| 探測庫 quota rows | 1,024 | 硬上限 1,024 |
| tool_versions | 16 | 硬上限 32；不可刪 referenced 身份 |
| 跨實例 active file leases | 2 | 1–8 |
| worker inventory page／結果 batch | 32／16 | 固定上限；batch 另限 2 MiB normalized payload |
| positive TTL | 30 日 | 1 小時–90 日 |
| negative retry TTL | 15 分鐘 | 1 分鐘–24 小時 |
| file lease | 30 秒 | 10 秒–5 分鐘，不超 parent 當前 lease |
| 清理每交易 | 128 rows | 最多 128；每輪最多 4 批，之後 yield |
| DB 操作 | 2 秒 | 沿用 worker bound，lock／statement timeout 一起設定 |
| 公共列表 | 50 | 1–100，只回 summary；單項 metadata 最多 128 KiB |

這些值約束可見 row 與序列化 payload／預留額度；不是 PostgreSQL 實體磁碟的精確 hard quota。索引、TOAST、MVCC dead tuples、WAL、備份及現有 inventory 另占空間；vacuum、磁碟容量告警仍需運維措施。大量不同庫不能靠「每庫有界」推導全域有界，因此必須同時有 global quota 與 quota-scope 上限。

滿額時先清到期且無有效 lease 的 row；再按 `last_used_at, root_id, path` 淘汰未持有 lease 的舊 row，每次最多 128。仍不足時回 `probe_cache_capacity`，不再啟動新程序，job phase 暫止／失敗為資源問題。不能以無界同步 eviction 或默默不保存後仍每輪重探取代 backpressure。1,000 檔「不變=0」驗收必須在容量容納所有檔案、TTL 未到期及無淘汰的條件下進行。

hit 的 `last_used_at` 最多每小時更新一次，避免每次 list／scan 都放大寫入；hit 不延長硬 TTL。清理不刪原檔、catalog 或 inventory baseline。

Inspect 的 byte／FD／batch 數量有界，正常檔案讀取支援 context 取消；網路檔案系統卡在底層 Open／Stat 等 OS 呼叫時，不能據此宣稱硬性 wall-clock 上限。這與程序 timeout／DB statement timeout 是不同邊界。

## 6. 租約、取消與短交易

每一次 reserve、heartbeat、commit、release、checkpoint 需要同時滿足：

```text
parent jobs.state = running
parent owner / generation = supplied lease
parent lease_until > clock_timestamp()
parent cancel_requested = false  (cleanup release 除外)
cache lease owner / generation / parent identity 相同
cache lease_until > clock_timestamp()
library / root / item generation 仍為預留時版本
tool / parser / fingerprint identity 仍符合 phase
inventory entry 仍在相同 job、同 root/path、同 size/mtime
```

同一路徑的 lease 由 PK row lock 保證唯一，不因兩個 caller 提交不同版本而同時探測。遇到其他有效 owner 回 `busy`，不得另啟程序；沒有有效 lease 時才可遞增 generation 搶回。新 owner 的 generation 使舊 worker 的提交失敗。expiry 用 DB 的當前值，不使用 lease 結構內過期的 ExpiresAt；parent heartbeat 可在同交易延長其最多 8 個 child leases，且不超 parent 新期限。

快取交易保持既有鎖順序：公共管理先 live session／admin 驗證，再 jobs advisory lock，再 global quota、library quota、scope rows，最後按 `(root_id,path)` 排序鎖 cache rows。worker 省略帳戶鎖，其餘同序。清理也從 jobs advisory lock 開始，不能先鎖 cache 再反向等待 jobs。此初版較保守地序列化短 DB 寫入；FS、SHA、JSON parse、ffprobe、sleep 均在交易外。

`CommitProbeBatch` 先驗證所有結果的型別／數量／serialized size，再開交易；驗證目前 cursor 與預期連續 prefix、所有 fence、庫／條目 generation，更新 cache、quota、進度。交易尾再以 `clock_timestamp()` guarded UPDATE 重查 parent／child expiry 與取消，任何一項失败全部 rollback。不能只在交易開始檢查租約。

取消若先於 commit 交易取得 jobs 鎖，commit 拒絕；如果結果先提交，之後的取消不回溯已完成的 cache observation。worker 觀察到取消立即停 process；cleanup 不寫媒體失敗，只釋放 child leases，再按既有規則 Finish cancelled 或 Release parent。heartbeat／lease 遺失時不自行提交成功或失敗，等待 DB expiry recovery。

回收程序同時清掉 expired／已非 running parent 的 child leases、釋放預留容量，再允許 parent history trim。`RESTRICT` FK 提供最後保護；不可只靠 cleanup job 遲早處理，否則既有 trimJobs 會被孤立 lease 阻塞。

## 7. 錯誤分類與負快取

| 事件 | 持久化行為 | job 行為 |
| --- | --- | --- |
| 有效 JSON／來源穩定 | ready metadata，positive TTL | 繼續 |
| ffprobe 正常 exit 1、確認個別媒體不能解析 | failed／`probe_failed`，短 retry TTL | 計 failed，繼續下一檔 |
| JSON invalid／JSON limit／程序 output limit | failed，分別固定 `probe_metadata_invalid`／`probe_metadata_limit`／`probe_output_limit` | 繼續；不可保存部分 metadata |
| 單次媒體執行超時、parent context 仍有效 | failed／`probe_timeout`，短 retry TTL | 停該程序、繼續 |
| source changed／消失／無法安全開檔 | phase changed／unavailable 計數，該候選不保存失敗 metadata | 繼續；下次重新枚舉／Inspect |
| runner busy／尚有別的 path lease | 不做負快取；不推進該未完成 prefix | bounded backoff 或讓出 worker |
| helper 64／78、異常退出或訊號、啟動／清理／平台失敗 | `probe_runtime_unavailable` 記在 phase；不得把任何 cache 改成 probe_failed | 停該 phase，避免整庫逐檔重試故障 runtime |
| parent cancel、shutdown、parent deadline、lease lost、DB failure | 不產生媒體失敗 | 沿既有 cancel／release／recover；DB failure 不誤算壞檔 |

Adapter 將工具內部 timeout 映成 `context.DeadlineExceeded`，所以 orchestration 必須同時檢查 parent context：parent 已到期是 job lifecycle；parent 尚有效才是該媒體探測 timeout。這項差異要有測試，不能只看 error 字串。

單項 negative cache 必須以穩定的預檢 stamp＋失敗後再次 Inspect 一致為前提。Adapter 錯誤仍回傳空 Observation，不能為了負快取破壞其契約。無法再次證明檔案版本一致時，只計 changed／unavailable，不綁定舊 stamp 保存 probe_failed。

runtime unavailable 以既有 job `scan_unavailable` 結束，詳細固定原因存在 probe phase；若希望公開新的 job error code，必須由 004 明確 ALTER CHECK 與更新 domain／API，不能直接寫進 schema3 未允許的值。初版建議不擴大 jobs.error_code。runtime 恢復後由既有 Retry 建新 job；未改動且已提交的 positive cache 仍可命中。

負快取 retry 次數最多 10，TTL 有界；顯式管理員重建／失效可立即重試。工具／parser 身份或任何檔案鍵變化也立即 miss，不沿用舊負結果。單檔持續失敗不能阻塞其他項目，也不能使每次掃描永久無間隔地啟動同一程序。

## 8. 建議接口（待 root 確認）

以下為 domain／app 層型別；不讓 app 依賴 postgres 或 process。`ProbeSource` 是可信 resolver 的 private root path 與相對路徑；不得 JSON marshal 絕對路徑。`ProbeObservation` 可由現有 adapter Observation 轉接，避免改動已凍結解析器。

```go
type ProbeStamp struct {
    Size, ModifiedUnixNano int64
    Fingerprint [32]byte
    FingerprintVersion string
}
type ProbeObservation struct {
    Stamp ProbeStamp
    Metadata domain.MediaMetadata
    ToolIdentity [32]byte
}
type MetadataProber interface {
    Inspect(context.Context, ProbeSource) (ProbeStamp, error) // no child process
    Probe(context.Context, ProbeSource) (ProbeObservation, error)
}

type ProbeRepository interface {
    RegisterProbeIdentity(context.Context, ProbeIdentity) (ProbeIdentityRef, error)
    BeginProbePhase(context.Context, domain.JobLease, ProbeIdentityRef, ProbePolicy) (ProbePhase, error)
    NextProbePage(context.Context, domain.JobLease, int) (ProbePage, error)
    ReserveProbeBatch(context.Context, domain.JobLease, ProbePageToken, []ProbeCandidate) ([]ProbeDecision, error)
    CommitProbeBatch(context.Context, domain.JobLease, ProbePageToken, []ProbeCompletion) (ProbeProgress, error)
    ReleaseProbeLeases(context.Context, domain.JobLease, []ProbeLease) error
    FinishProbePhase(context.Context, domain.JobLease) error
    SweepProbeCache(context.Context, int) (ProbeSweepResult, error)
}

type ProbeAdminRepository interface {
    GetProbeSummary(context.Context, domain.Actor, ProbeTarget) (ProbeSummary, error)
    ListProbeSummaries(context.Context, domain.Actor, string, ProbeCursor, int) (ProbeSummaryPage, error)
    InvalidateProbeLibrary(context.Context, domain.Actor, string) error
    InvalidateProbeItem(context.Context, domain.Actor, string) error
    RebuildProbe(context.Context, domain.Actor, ProbeRebuildRequest, string, domain.JobPolicy) (domain.Job, bool, error)
}
```

- `ProbePageToken` 包含 expected cursor 與 phase revision，只是 worker 私有 CAS token，不能從 HTTP 接收；repository 仍重新查合法 inventory prefix，不能信任呼叫者提供的 list／root path。
- `ProbeCandidate` 包含 inventory ID、Inspect stamp；`ProbeDecision` 固定為 hit／negative_hit／leased／busy／capacity，且 leased 附加 `ProbeLease{rootID,path,owner,generation,parentID,parentGeneration,scopeGenerations,stamp,toolID}`。處理到第一個 busy／capacity 即停止，不為後方項目取得 lease；回傳 list 明確對應已處理的連續候選 prefix，不能重排或省略中間项。
- `ProbeCompletion` 是 tagged union：hit、negative_hit、成功 observation、固定 media failure、changed／input unavailable；每項最多一種 payload。busy／runtime unavailable／取消不能偽裝 completion 讓 cursor 跳過。
- `CommitProbeBatch` 必須防重放；預期 cursor 已前進時回傳 conflict 或已提交的相同結果，不重複累加。初版可統一返回 conflict，worker 重新讀 phase，不推測成功。
- `NextProbePage` 無工作回 ErrNotFound，但 parent lease／取消／phase 不符要優先回真正錯誤；`FinishProbePhase` 再驗證 cursor 已到符合 scope 的 inventory 結尾。
- heartbeat 直接擴充既有 `HeartbeatJob`，同交易更新該 parent fence 的 child leases，不另造第二個永久 monitor goroutine。
- 清理是可信服務內部工作；公開觸發仍走 admin 包裝與 rate limit。所有 SQL 值參數化，SQL 錯誤沿用安全 storageError。

## 9. 公共 API 與失效語義

第一版以管理員 API 提供 summaries／單項已規範 metadata、library／item invalidate 與 rebuild；每個 repository 公共操作必須交易內重新驗證有效 session、未停用／未刪除 user 與當前 admin。普通使用者不得藉 root ID／path 枚舉其他庫快取。未來一般 catalog 詳情若要讀 metadata，需另走 item／library ACL，不能直接暴露管理 API。

HTTP target 使用既有 library ID、item ID，或已授權的 inventory/source ID；不接受任意 root 絕對路徑、工具 path、argv、manifest 或 shell 參數。列表只回相對識別與安全 counter／code；日誌和 audit 不保存 filename tag、raw JSON、stderr 或本機 root。

| 失效觸發 | 行為 |
| --- | --- |
| size／mtime／edge SHA 改變 | 下次 Inspect miss；舊 worker 的候選版本不得覆蓋新 lease |
| rename／root 改變 | 新 path／root identity 是新 key；舊 row 等 TTL／清理，不刪原檔 |
| root path 改設定 | 同交易遞增 root generation；舊 row 即時不可命中／提交 |
| item 關聯建立／改變 | current item ID／generation 與 row 不符即 miss |
| admin library invalidate | library generation +1；已存在 inflight 提交失敗；不需要同步遍歷 cache |
| admin item invalidate | item generation +1；該 item 的全部 source 同時失效；不需假设 source 數量有限 |
| tool／runtime lib／parser／arguments／sandbox identity 改變 | identity 不符即 miss；不覆寫既有 tool_versions 身份 |
| TTL 到期 | 不再 hit；只刪無有效 lease 的 cache |
| runtime 故障 | 不清除所有已保存好結果，也不將整庫變 failed；新的 probe 停止 |
| 疑似大量原檔消失 | 保持 schema3 baseline／review；快取不得驅動 catalog／檔案刪除 |

rebuild 需 Idempotency-Key（沿用 1–128 ASCII bytes）。同 actor／key 的 library、item、mode、priority 不同要 conflict；同 body 重放返回原 job。將 scope 記在 probe_job_state，不能沿用只比 library／priority 的舊 SubmitJob 檢查而漏掉 item／mode。

rebuild 的 epoch bump、job 入列與 audit 必須同交易。若同庫已有 active job 或 queue 滿，整個交易回退，不能先失效後才報無法重建。item rebuild 可沿同庫 inventory 掃描，再只 probe 該 item 當前 media_sources 對應的 inventory 項目；初版不宣稱只掃單一目錄。invalidate 本身可在工作進行中執行；舊 phase 收到 generation conflict 必須停下／重建，不能自動把舊 Observation 改成新 generation。

## 10. 驗收方法：1,000 → 0 → K

以下是要新增的真實驗收，**尚未執行**。使用獨立 PG schema、臨時媒體庫與受隔離的實際固定 ffprobe。測試完成清理自建服務／schema／檔案，絕不使用使用者媒體。

1. 用既有固定開發工具生成原創、合法的 MP4 素材 A／B；測試不下載或使用外部影片。記錄兩者完整 SHA256、size、mtime、edge fingerprint 與解析結果。
2. 建立 1,000 個不同相對路徑，各指向 A 的 hardlink（不支援 hardlink 時用副本）。hardlink 的 inode、size、mtime、fingerprint **相同**；不聲稱有 1,000 種不同內容。由於 key 含相對路徑，預期 1,000 個 cache entries、1,000 次 metadata 探測。跨路徑內容去重不在本階段。
3. 啟用足以容納 1,000 rows／payload 的 quota、未到期 TTL，完成首次 inventory＋probe job。核對成功 metadata 1,000、failed 0、lease 0、精確 quota 與 cursor。執行計數以實際 metadata runner 呼叫為準，固定 runner 每次最多執行一次工具、沒有隱藏 retry；每次呼叫必須獲得真實 ffprobe 的有效結果。版本診斷／sandbox health 的啟動次數另計，不能混進 metadata 次數。
4. 立即再掃描而不動檔案／身份／policy：Inspect 1,000 次，但 metadata runner 增量 **0**；1,000 hit。root／path 相同且所有 stamp 相同；不是因 skip 整個目錄而假裝命中。
5. 選固定 K，例如 17 個路徑。**不能原地改寫共用 hardlink**，否則其餘 983 也會一起變。為每個 selected path 先建立 B 的新檔／hardlink，再用 atomic rename 替換 A 的那個 directory entry；B 與 A 的合法影片尺寸／內容不同，size／edge 指紋亦不同，mtime 設為可精確重現的另一值。逐路徑確認只有 K 個完整 key 改變，其他 1,000−K 的 size／mtime／edge 和完整 SHA 不變。
6. 第三次扫描預期 metadata runner 增量恰好 **K**、hit 恰好 1,000−K，所有 metadata 與 A／B 預期相符；完成後全部原檔 SHA 與預先準備的 A／B 相符。
7. 另分開驗證「只改 mtime」及「同 size／mtime 改 edge」都 miss；同 size／mtime 的中段-only 修改仍是已文件化的不保證範圍，不把它列成必須偵測的 acceptance。

同一原創素材的 1,000 條路徑驗證 cache／增量成本，不代表 1,000 種 codecs 的相容性或真實片庫效能。報告必須列明 hardlink／copy 比例、實際 identities、工具呼叫數、峰值並行、行數、payload bytes、耗時與測試 filesystem；不能只報 mocked counter。

## 11. 必須補的 PG／worker／API 回歸

- migrations 4→3→0→4；000001–000003 原文 hash 不變；schema3 inventory／baseline 在 down4 保留；down4 丟失 cache 的限制明確。
- 兩個 DB connections 同 path reserve 只有一個勝出；不同 library／path 不互相 hit。quota 臨界競爭、reserved payload、tool_versions 上限與 cleanup 精確差額。
- parent generation reclaim、cache generation reclaim、cache row 刪除再建立的 ABA、租約在交易中到期、cancel 在最後 heartbeat 後到達、item／library invalidate 在 process 執行中到達：舊 worker 的 metadata／counter／cursor 全部不能提交。
- process 成功但 commit 前 crash；restart 重做未提交 prefix；commit 成功後 crash 不重探已 checkpoint 項。cursor 排序／連續 prefix／batch 重放／任意偽造 inventory ID 都驗證。
- history trim 不刪活躍引用，terminal cleanup 與 retention 並發無孤立 lease；cache 不依賴被清掉的 inventory job。
- corrupt 單檔 negative cache，其他 999 繼續；TTL 到期或 force rebuild 只重試匹配項。invalid JSON／超限／child timeout 逐項分類；parent timeout／取消絕不冒稱 probe_failed。
- helper unavailable／signal／unexpected exit／cleanup failure：立即停止 phase，沒有整庫 failed；已有正常 cache 不受破壞；健康恢復後重試可用。
- live admin 撤權／session 撤銷後 get/list/invalidate/rebuild 全拒絕；普通使用者與跨庫 ID 不洩漏 cache；HTTP response、log、audit 無絕對 root／raw tags／stderr。
- 清理每批 ≤128、每輪 ≤4 批；遇 active lease 不驅逐；容量不足停止啟動程序；global rows／bytes 在並發完成後不超上限。實體 PG 磁碟不是該 counter 的等價值。
- disabled probing 時完全維持既有純 inventory 行為；Windows／不具 sandbox 能力的環境明確 unavailable，不退回裸 ffprobe。

## 12. 已選定的接口方向

1. 同一 inventory job 的 opt-in probe phase，以重用 lease／取消／同庫唯一和已完成 inventory。
2. `Inspect + Probe`、private page／file lease 與有界 batch，同時驗證 parent／child fence 的 commit 是唯一持久化入口。
3. cache 單版本、global＋library quota、scalar generation 失效。表中數值仍是實作前的明確建議範圍，後續依驗證落實，不自動等同已支援配置。
4. 初版 runtime unavailable 結束為既有 scan_unavailable，精確原因保存在 probe phase，不擴大 schema3 job error enum。

方向已確認；尚未將 004 migration／cache repository 加入正式原始碼，也未啟用掃描中的生產 probe、變更 HTTP 或執行本方案验收。一般接口細節可依上述方向持續設計；不把後續實作停在等待使用者批准。
