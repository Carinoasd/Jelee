# 合併忽略與混合媒體的持續暖掃描

在既有受保護容器的 1,000／100 媒體驗收中，保留冷掃描、暖掃描、受控替換後重掃、取消恢復與 SIGTERM。啟用明確持續驗收模式後，在替換重掃成功之後，使用同一 worker、HTTP 實例及 PostgreSQL schema 連續暖掃描至少五分鐘，至少完成五輪，不以等待填補負載時間。

每輪仍核對實際 NFO 完整讀取與雜湊、零重新解析、零新增 metadata probe 子程序、所有圖片 unchanged、規則來源與排除檔不進入快取／基線，以及 SQL quota／invalid／lease 數量。不得把暖掃描的完整讀取說成只讀變更檔。父程序 Go heap 每輪抽樣上限 256 MiB，結束 GC 後增量上限 64 MiB，goroutine 增量不超過 8；這些不是整體 RSS 數據。

目標 family-ignore-sustained-worker-test 分別執行兩規模的固定五分鐘模式，使用既有 readonly root／media、UID65532、無 capability、no-new-privileges、2 CPUs／768 MiB／128 PIDs 配置。只替換指定自有 NFO／圖片素材，原影片與素材 hash 仍保持，UUID 容器／映像／schema 仍須清理。新增 CI 必要步驟與證據保存。持續模式拒絕覆寫舊證據；一般三輪驗收仍使用原目標。

本段沿用正式 worker、真實 HTTP／PG／NFO／probe 與 Fx lifecycle 的測試建構接縫，包括完成真實 NFO 讀取後的取消屏障。它不是公開 New 的完整圖替代證據；公開圖有另外的原生驗收。兩規模驗收通過；G22 與全案仍部分完成。

## 實測結果

| 規模 | 持續負載 | 額外暖掃描 | 父 Go heap 峰值抽樣 | Goroutine |
| --- | --- | --- | --- | --- |
| 1,000 檔 | 303.903 秒 | 20 輪 | 3,260,656 bytes | 13 → 13 |
| 100 檔 | 300.392 秒 | 170 輪 | 3,580,976 bytes | 13 → 13 |

兩規模的每輪暖掃描均完整讀取／雜湊 NFO 800／80 次，重新解析及 probe 子程序啟動均為零；圖片 500／50 個均 unchanged。取消恢復保留成功基線，SIGTERM 後 HTTP 關閉、活躍 NFO／child／lease 均為零。原素材與影片 hash、測試 sourceDigest 保持一致，自有 schema／容器／映像已清理。完整逐輪結果見[驗收證據](evidence/ignore-family-mixed-sustained.json)。

Windows 全 Go 測試、runtime probe tag 服務測試與 vet、三命令 build、Go 格式及 Python／YAML 檢查通過。正式 worker、PG 與遷移沒有改動；完整品牌門禁仍有歷史殘留。
