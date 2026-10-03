# 大圖片完成後的記憶體回收

正式24h來源9a74a8932a在38輪後RSS超過464MiB；[原始失敗摘要](evidence/image-soak-rss-failure.json)保留完整原因與數值。此修復先處理可以隔離重現的圖片分配／回收問題，正式長測尚未重新通過。

## 根因與修復

相同PNG16／PNG／JPEG模板、兩個worker及原GOGC100／512MiB soft limit，在獨立Linux圖片adapter中1198張就重現RSS498192384 bytes。停止後強制GC，heap降到342720 bytes但RSS仍498786304；再交還OS頁面才降到8724480。GC前的inuse_space採樣主要為PNG解碼的兩份80MiB位圖；GC後已不在profile中。這支持解碼物件已死亡但Go仍持有其頁面的根因，不能把舊GC週期的profile當成持續存活的快取。

解碼／縮放／編碼改在獨立`renderDecoded`函式內完成，只回傳緊縮JPEG。估算達`min(64MiB, maxImageBytes/2)`的大操作，在該函式返回後檢查Go管理的總記憶體減已交還頁面；超過原有「並行槽數×單圖預算＋快取容量」才同步呼叫`debug.FreeOSMemory`。驗收配置的壓力值為224MiB。它是Go管理記憶體的壓力判斷，RSS仍由外部驗收獨立量測。

回收完成前保留CPU許可與圖片槽；取消、解碼失敗及Shutdown都等待回收join。小圖與cache hit不觸發此步驟。沒有新增背景回收goroutine，不改GOGC／GOMEMLIMIT、兩槽並行、每圖96MiB、快取32MiB、圖片來源或RSS／GC門檻。

## 隔離證據與限制

| 實作 | 完成張數 | RSS抽樣峰值 | GC counter 暫停比例 |
| --- | ---: | ---: | ---: |
| 原版 | 1198後停止 | 498192384 bytes | 失敗負載，不能作完整比較 |
| 每張大圖都回收的中間版本 | 10000 | 186642432 bytes | 約1.78%，未採用 |
| 按壓力回收的最終版本 | 10000 | 352055296 bytes | 約0.642% |

三次使用同一診斷程式與模板SHA256；每輪1000張，連續十輪、100ms採樣。負載沒有HTTP／PG／掃描／KDF／五分鐘idle，且是原生Linux程序，沒有容器限制；不當成正式十萬圖片或24h驗收。GC直方圖門檻與混合工作延遲仍需原正式驗收。同步回收會觸發程序GC；按壓力執行減少不必要的回收，完整混合負載影響仍待驗證。

Windows圖片／HTTP／architecture540通過事件、1平台跳過，runtime214通過／20條件跳過；Linux race對應三套件526通過／零跳過、runtime227通過／13條件跳過；真PG runtime metrics另1通過／零跳過。新增紅測試先證實失敗與取消沒有回收；修後核CPU／圖片許可、取消／Shutdown join，以及小圖／cache hit保持。vet、格式、增量品牌0／339、gitignore、diff檢查通過，98份發布SQL保持。

[安全數值證據](evidence/image-reclaim-pressure.json) · [GC前profile摘要](evidence/image-reclaim-heap-before.txt) · [GC後profile摘要](evidence/image-reclaim-heap-after.txt)

## 重現隔離負載

[Go診斷程式](evidence/image-reclaim-reproducer.go.txt)與[Linux runner](evidence/image-reclaim-runner.py.txt)可複製到既有checkout的`.testdata/image-rss-repro/main.go`與`.testdata/run-image-rss-repro.py`，再於Linux執行`python3 -B .testdata/run-image-rss-repro.py`。它使用已鎖定toolchain，另建owned臨時夾具與scratch，保存私有heap profile及數值摘要，結束清理臨時目錄；不使用資料庫或原媒體。

原版對照使用aaee979084的processor.go；可以由Git讀出該檔並使用Go overlay編譯，保持既有checkout。已发布的報告SHA對應當次紀錄；重跑目前修復版會得到新的資料，不能覆寫當次失敗證據後宣稱來源相同。

下一步以已提交修復來源跑完整600秒smoke，只有來源／快照／雙層清理都通過，才用同提交啟正式24h。十萬圖片、GC直方圖、其他G00–G51與完整品牌門禁保持。
