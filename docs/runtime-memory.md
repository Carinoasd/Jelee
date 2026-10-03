# 執行時記憶體設定

`deploy/docker-compose.memory.yml` 提供明確選用的 `jelee` 記憶體設定。`GOGC=100`、`GOMEMLIMIT=512MiB`、容器 768 MiB 已通過本頁所述的混合 worker 負載驗收；相同負載的 `GOGC=50` 比較組也通過。結果與設定範圍見[實測證據](evidence/runtime-memory.json)，不代表所有合法配置都有足夠容量。

## 啟用與覆寫

從專案根目錄執行，先依[部署說明](deployment.md)設定資料庫密碼、媒體路徑及允許的 Host，再將 override 放在基礎檔之後：

```sh
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.memory.yml config --quiet
docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.memory.yml up --build -d
```

只使用基礎 Compose 檔時，不會啟用這份記憶體設定。兩份檔案合併後，新增欄位僅作用於 `jelee`；基礎檔的唯讀根目錄、唯讀媒體、capabilities 移除、no-new-privileges、tmpfs 與環回埠綁定繼續保留。[Compose 合併規則](https://docs.docker.com/compose/how-tos/multiple-compose-files/merge/)依環境變數名稱合併 `environment`。

可在執行 Compose 的 shell 或其 `.env` 檔覆寫以下三個值；`.env.example` 留有註解範例。Compose 的 `.env` 用於插值，不代表在宿主機執行的 Go 程序會自動載入該檔。

| 變數 | 預設 | 用途 |
| --- | --- | --- |
| `GOGC` | `100` | Go GC 的 heap 成長目標百分比 |
| `GOMEMLIMIT` | `512MiB` | 每個 Go 程序的 soft memory limit |
| `JELEE_CONTAINER_MEMORY_LIMIT` | `768m` | `jelee` 容器的 memory 與 memory+swap 上限 |

若較重視記憶體用量，可在 Linux shell 只覆寫 `GOGC`，保留本次比較組使用的 512 MiB soft limit 與 768 MiB 容器上限：

```sh
GOGC=50 \
  docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.memory.yml up --build -d
```

也可一併覆寫 `GOMEMLIMIT` 與 `JELEE_CONTAINER_MEMORY_LIMIT`。設定檢查已驗證 `50`／`384MiB`／`640m` 能正確合併，但該組較小上限未做負載驗收；調整後須重新量測。`mem_limit` 與 `memswap_limit` 共用同一變數，因此調整容器上限時仍維持兩者相等、禁用此容器的 swap。[Docker 的 memory／swap 規則](https://docs.docker.com/reference/compose-file/services/#memswap_limit)說明兩者相等時沒有可用 swap；本次容器也已核對 `memory.swap.max=0`。

## Soft limit 與容器上限

Go 在程序啟動時讀取原生 `GOGC`／`GOMEMLIMIT`，本段沒有增加會改整個程序的 GC setter。Soft limit 是 Go runtime 的記憶體管理目標，並非整個容器的硬上限；它不包含所有映射、外部工具、kernel 或其他程序的記憶體。Go 可以超過此目標，不能靠它保證沒有 OOM。[Go GC 指南](https://go.dev/doc/gc-guide#Memory_limit)說明這個限制。

容器總用量還包括同容器的 helper／ffprobe，以及被 cgroup 計入的 file cache、tmpfs 等記憶體。既有 ignore helper 自行設定 128 MiB Go soft limit，屬於獨立程序；主程序的 512 MiB 不能拿來當所有 helper 的合計上限。基礎 Compose 的 64 MiB `/tmp` 是容量上限，不是預先配置，但其實際使用也需計入容器預算。

PostgreSQL 在另一個容器或外部服務；這份 override 不限制 `postgres` 或 `migrate`。主機容量規劃須另計資料庫與其他服務。此檔沒有設定 CPU 配額；本次驗收另外限制容器為 2 CPUs、128 PIDs，未驗證整台 4C8G 主機的容量。

## 不輸出機密的 Compose 檢查

以下檢查已通過。腳本以固定假密碼、暫存媒體路徑及空的 env 檔執行真正的 `docker compose config --format json`，比對基礎檔、預設 override 與 `50`／`384MiB`／`640m` 覆寫三組。它確認只增加預期的 `jelee` 記憶體欄位，其他服務與安全設定維持原值；僅輸出安全摘要，不印完整 model、環境或 CLI 錯誤內容。

```sh
python3 -B scripts/check_memory_compose.py
```

這項檢查不啟動容器，僅證明設定解析與合併。執行限制由下面的容器驗收另行核對。日常查驗也應避免直接輸出完整 `config`、`config --environment` 或 `docker inspect`，其中可能包含資料庫連線字串。

## 本次負載與實測結果

兩組使用相同來源與負載，各自建置並執行 Linux amd64 容器。worker 以 UID/GID 65532、唯讀根目錄與媒體、移除全部 capabilities、no-new-privileges、2 CPUs、128 PIDs、768 MiB memory 上限及零 swap 執行；PostgreSQL 在容器外。每組包含 400 個 NFO、100 個影片與 500 個圖片 fixture，驗證冷掃描、暖快取、受控替換、取消復原及 SIGTERM 收尾。

每組另完成 19 輪暖快取掃描，持續約 309 秒。每組完成四次正式 Argon2id 操作（兩次 hash、兩次 verify），確認兩個 KDF 槽同時被占用，每槽使用 64 MiB、3 次迭代、parallelism 2。KDF 期間以真實 NFO 讀取完成後的 API 等待點維持 worker 在途；此等待點未模擬阻塞中的作業系統 I/O。

| 指標 | GOGC=100 | GOGC=50 |
| --- | ---: | ---: |
| GOMEMLIMIT | 512 MiB | 512 MiB |
| 容器 memory 上限 | 768 MiB | 768 MiB |
| cgroup `memory.peak` | 518.171875 MiB | 272.273438 MiB |
| 記憶體觀測區間耗時 | 401.190 秒 | 396.812 秒 |
| 區間累計配置量 | 1550.019554 MiB | 1581.166565 MiB |
| 區間 GC 次數 | 631 | 1708 |
| 區間 GC 累計暫停 | 33.360224 ms | 86.639246 ms |
| 19 輪暖快取掃描耗時 | 309.066 秒 | 308.580 秒 |

`memory.peak` 是容器 cgroup 的峰值，包括其子程序及被計入的 cache／tmpfs；不能當作父程序 RSS 或 Go heap。配置量、GC 次數與暫停時間取自 worker Go 程序起訖計數器的差值。兩組都在該程序內讀取 `/gc/gogc:percent`、`/gc/gomemlimit:bytes` 核對有效值。

兩組 worker 的 cgroup `oom`、`oom_kill`、`oom_group_kill` 增量均為零，容器正常退出且 `OOMKilled=false`。另外各自啟動正式 `/jelee` 入口，通過 `/healthz`、`/readyz` 與停止驗收；該入口的啟動環境已核對，GC 指標來自同容器內的獨立 observer 程序，未直接量到 `/jelee` 程序內的有效 GC 設定。

獨立的 OOM 負向容器限制為 64 MiB、零 swap、1 CPU、32 PIDs，沒有網路。它先確認配置階段標記，再嘗試配置 128 MiB，最後由 Docker 確認 `OOMKilled=true`、退出碼 137；不與正常負載容器共用此限制。兩組驗收自建的 schema、容器與 image 均已清理。

## 調整依據與重跑方式

保留 `GOGC=100` 作為 override 預設：本次峰值距 768 MiB 硬上限約有 250 MiB 餘量，且 GC 次數與累計暫停較少。`GOGC=50` 在本次負載的峰值較低，供重視記憶體用量者覆寫。每組僅跑一次，耗時接近，不能推論 CPU 使用或效能已改善；也不能把這段餘量視為更高併發或更大媒體的容量保證。

512 MiB soft limit 與 768 MiB 容器上限分別延續既有[掃描規模基線](scan-repeated.md)及[混合 worker 設定](nfo-worker-verification.md)，本次補上固定負載的真容器證據。調高密碼成本、worker 數或其他工作上限後，須重新分配預算並量測。

在已備妥工具、媒體 fixtures、Linux Docker 與專用 `jelee_test` PostgreSQL 的環境中，以 `JELEE_TEST_DATABASE_URL` 提供測試連線後，可從專案根目錄重跑完整入口：

```sh
make runtime-memory-test
```

此入口先跑 contracts，再執行兩組真容器負載。報告保存在忽略的 `.testdata/runtime-memory-*`；重跑前先另存既有驗收證據，controller 會拒絕覆寫。CI 分開執行 contracts 與負載，失敗時也保存安全摘要與可取得的日誌。本次容器量測結束後，僅補修 Python 的失敗日誌保存，Go 與部署來源未變；該修補的 mock 回歸另行記錄，不將它宣稱為再次完成容器量測。

本頁完成的是固定配置的短期記憶體／OOM 驗收。後續 G42.9 已另以實際父程序 RSS 基線制定固定預算並通過獨立負載與超標門禁，見[常駐記憶體預算](resident-memory.md)；Go heap、RSS 與容器硬上限各自核對。此次只有 500 個圖片 fixture，沒有十萬圖片或 24 小時穩態證據，也未驗證全部工作類型與所有合法配置。
