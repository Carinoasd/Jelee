# 合併忽略模式的混合掃描驗收

正式 PostgreSQL、HTTP、app 准入、worker 與 Fx 生命週期在受保護 Linux amd64 容器執行兩個規模情境：1,000 筆及 100 筆媒體。使用實際固定 probe 工具、原生忽略 helper、NFO 讀取解析與圖片屬性比較。容器 UID 65532、唯讀根與媒體、移除 capabilities、no-new-privileges，限制 2 CPU／768 MiB／128 PID。生命週期透過既有驗收工廠組裝；正式 New(cfg) 的公開服務驗收另見 [runtime 證據](ignore-family-runtime.md)。

## 兩家族規則

額外建立兩個規則檔及三個故意無效的排除檔案：

```text
.jeleeignore:
ignored-video.mp4
!video-*

.ignore:
ignored-*
video-*
```

正常影片必須通過自有 include，覆蓋舊規則的 exclude。故意無效的影片由自有規則排除；無效 NFO 與圖片由舊規則排除。每轮報告精確核對三個 scan 排除、家族、rule 原因、行號 1、相對來源目錄與命中路徑。三個檔案都未進入 NFO／probe 快取或掃描基線。

## 實際增量計數

每個情境執行冷掃描、暖掃描與受控替換後掃描。以下數字來自正式服務計數器與已提交的資料庫摘要。

| 媒體筆數 | NFO／影片／圖片 | 三輪 NFO 解析次數 | 三輪 probe 子程序啟動 | 最後一輪圖片變更／未變 |
| --- | --- | --- | --- | --- |
| 1,000 | 400／100／500 | 400／0／17 | 100／0／0 | 23／477 |
| 100 | 40／10／50 | 40／0／3 | 10／0／0 | 4／46 |

三輪每輪仍完整讀取並雜湊全部納入的 NFO 兩次（800／80 次），以驗證內容與發布前一致性；暖掃描零重新解析。檢查來源內容 hash、UTF-8／UTF-16LE／UTF-16BE／GBK、XML／語意無效的負快取、多集與警告摘要、快取 quota 和零殘留 probe lease。每輪結束 NFO ActiveCalls 與 probe Active 都為零。

## 取消、停止與保護

NFO 實際讀取後放置可取消的 API 屏障，取消工作保留原圖片基線、Missing=0，再經原生完整掃描恢復完成圖片比較；恢復沒有重新解析 NFO 或啟動 probe。另一屏障讓控制器向容器送 SIGTERM，Fx 停止後 HTTP 關閉、NFO 呼叫與 probe 子程序為零、owner 與 probe lease 為零。屏障沒有正在執行的 probe／忽略子程序，不作活躍子程序中途取消聲明。

控制器只替換自有 fixture 的指定 NFO 與圖片，媒體挂載唯讀；原始合成素材 hash、影片內容與測試期間來源 hash 均保持。兩個 UUID 容器、映像與專用 schema 均已清理。其他容器與資料庫保留。

## 證據與入口

[原生受保護容器證據](evidence/ignore-family-mixed.json)包含兩規模、六輪實際計數、取消恢復與 SIGTERM 結果；均 passed、sourceUnchanged=true、testArtifactsCleaned=true。1,000 筆三輪耗時 36.664／18.389／19.442 秒；100 筆為 4.434／2.778／3.682 秒。

```text
make family-ignore-worker-test
```

使用既有專用 PostgreSQL 設定，控制器啟用 JELEE_FAMILY_IGNORE_ACCEPTANCE。CI 新增同一必要驗收及日誌／摘要保存，原 NFO 與 probe 驗收仍執行。完整 Go 測試、vet、三命令 build、probe 測試 tag 编译、Python 與 workflow YAML 語法檢查通過。Linux runtime／jobs／architecture race 亦通過，依序 1.105／1.356／1.215 秒。Windows 沒有真實 PostgreSQL 整合聲明。

其他歷史專用忽略格式、活躍 helper 的服務完整鏈取消、規模壓力與長時間穩定性仍待驗收，G22保持部分完成。
