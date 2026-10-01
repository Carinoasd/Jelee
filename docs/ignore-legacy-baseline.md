# 舊格式基線候選的來源觀察

固定包裝器 blob `023c1e891532e5baa022ef0e48b7179e991f59e3` 的冷查找以 `DirectoryInfo.Parent` 往祖先逐層檢查 `.ignore`，沒有要求起始目錄必須存在。此段核對該版本的 `FindIgnoreFileCached`；不採用其他版本的目錄匹配政策。

## 缺失邊界

`ObserveLegacyBaseline` 保留最深仍存在目錄的完整來源鏈，以及第一個確認不存在的子目錄。缺失必須由已持有父目錄的 `OpenDirectoryOrAbsent` 確認；權限失敗、非目錄、連結及讀取錯誤仍回錯誤。缺失點之下不製造身份或來源不存在的證據。

最近來源從仍存在的最深目錄向根查找；空檔仍是存在的來源，遮蔽祖先。兩轮原生開啟、身份、來源內容與缺失邊界都必須相同，所有持有 handle 成功關閉後才回傳。原本一般掃描的 `ObserveLegacy` 仍要求目錄存在。

領域 `LegacyIgnoreBaselineObservation` 採獨立版本 `legacy-baseline-source-v1`，包含原始 lookup 目錄、既有最近來源觀察與獨立缺失邊界。缺失邊界必須緊鄰來源鏈尾，且位於 lookup 路徑上；不能拿另一個子目錄的不存在證据代用。無缺失邊界時，lookup 必須等於來源鏈尾目錄。

## 匹配

`MatchLegacyIgnoreBaseline` 對共用 lookup 目錄的最多128個候選使用原有解碼、完整路徑正規化、受限 helper 與回覆驗證。匹配前後重新觀察來源與邊界，任一變化即拒絕整批。一般匹配與基線匹配共用相同解碼／helper 函式。

原生及匹配器層只提供規則決定，不能單獨宣告檔案從 inventory 消失。資料庫仍需完成目錄覆蓋、基線快照比對與最終來源驗證。本段尚未保存新的缺失邊界或啟用合併基線分類，schema 保持15。

## 驗證

- 祖先來源、近層空來源、遮蔽不安全祖先、已存在目錄無來源。
- 第一缺失子目錄的父身份、禁止虛構更深證據、資料脫敏。
- 兩輪之間缺失父目錄重現、匹配期间重現、非目錄不可視為不存在、取消。
- 真實 helper 對已消失父目錄下的候選匹配，保留來源及規則行號並確認程序結束；CI 另跑非 race 驗證。

本段 Windows source／scan／domain／architecture 測試通過（1.132／1.656／0.188／0.170 秒），全模組 vet 及三命令建置通過。Linux 原生 `/tmp` race 分別為10.205／1.185／1.055／1.092秒；真實 helper 非 race 匹配0.007秒通過。增量品牌掃描0違規／100合法命中、gitignore-check與diff-check通過。

## schema16 保存與還原

`RecordLegacyIgnoreBaselineObservations` 每批接受最多128筆同根來源查詢。既有來源鏈仍存入舊格式來源清單，原始 lookup、最深既有目錄及第一缺失邊界存入新表 `job_ignore_legacy_baseline_queries`。缺失邊界不會寫成 identity=0 的既有目錄證據。

來源與新查詢共用回滾點。批次內部、與已保存查詢、或「曾確認不存在的目錄又被觀察為存在」發生衝突時，撤回本批新增來源與查詢，只保存失效標記。相同查詢重播不重複計費，凍結後拒絕追加；新查詢最多16384筆，計費與來源清單共用64MiB上限。

`ReadLegacyIgnoreBaselinePage` 從凍結且有效的清單按 root／lookup 游標每頁還原最多16筆，集合讀取並還原共享祖先。`ReobserveLegacyIgnoreBaseline` 重新原生觀察完整來源与缺失邊界；父目錄重現、來源改變或身份改變都會拒絕。這是單次復查，整個任務的持久復核進度與發布守衛仍待接線。

新增遷移16，既有001–015保持。存在基線查詢時拒絕降版，刪除任務可連帶清理後再降版。原始查詢與邊界不可更新，讀取時重新驗證領域合同。

schema16 本段 Linux 真實 PostgreSQL 全套 race：213 項頂層測試通過，0 失敗、0 跳過，278.475 秒；受測 PG 來源前後雜湊一致（`.testdata/inventory-legacy-baseline-full-postgres-summary.json`）。第一輪舊來源／新基線20項整合測試亦通過（36.099秒）。包含18次查詢跨頁還原、凍結重播、兩種寫入順序及同批存在／缺失矛盾、預算、租約／取消／世代、升降版與原生復查。Windows postgres／scan／architecture 測試、全模組 vet、三命令建置、增量品牌掃描及gitignore-check通過。
