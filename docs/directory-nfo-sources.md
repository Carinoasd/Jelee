# 影集與季的資料夾來源

第39版讓 Series／Season 使用獨立的 `item_directory_sources`，對應真實根目錄及相對資料夾。影片仍使用 `media_sources`；同一條目混用兩種來源時，NFO resolver 拒絕選擇。

## 登記與父子關聯

```sh
jelee-cli import-directory --library "影集" --root /media/series \
  --directory Example --title Example --kind Series

jelee-cli import-directory --library "影集" --root /media/series \
  --directory "Example/Season 01" --title "第一季" --kind Season \
  --parent SERIES_ITEM_ID

jelee-cli import-video --library "影集" --root /media/series \
  --file "Example/Season 01/Example.S01E01.mkv" --title "第一集" \
  --kind Episode --parent SEASON_ITEM_ID
```

Season 必須指定 Series 父層；Episode 可指定 Series 或 Season。父子必須同庫、同根，子位置位於父資料夾內。資料庫以種類與媒體庫複合外鍵拒絕跨庫或不合法種類關聯；種類規則也排除了循環。目錄列表與單項 API 在有關聯時回傳 `parentId`。

CLI 要求資料夾存在，根目錄本身與根內路徑元件不得是符號連結。登記只保存位置與關聯，不讀寫 NFO；條目、來源、父子關聯及稽核同一交易，失敗全部回滾。

## NFO 契約

Series 資料夾只選 `tvshow.nfo`，Season 只選 `season.nfo`，大小寫不敏感。同名歧義、不安全來源及資料夾替換均拒絕。讀取會核對根、資料夾與 NFO 的實體身分，重新檢查不會只比較字串路徑。

Season 使用 `season-details-v1`，共用電影的28欄加 `seasonNumber`，合計29欄。編號為0–1,000,000，保留缺省與零；`seasonnumber` 沿用既有 SeasonNfoSaver.cs 的輸出。人工清除、值來源及獨立鎖共交易；IndexNumber 鎖可在沒有編號時獨立保存。

API facts 聯集仍30項、16種變體；新增投影不改寫既有投影語彙。保留資料夾來源、父子關聯或新季投影證據時拒絕降版；第1–38版76份 SQL 保持原樣。

## 驗證狀態

完整本地驗證已通過；遠端 CI 以推送後結果為準。

自動命名辨識、全庫匯入、監看／排程、前端階層操作與無損寫回仍待後續。原媒體、NFO及圖片保持原內容。

## 驗收結果

| 驗證 | 套件 | 通過事件（含父測試） | 略過 |
| --- | ---: | ---: | ---: |
| Windows 全套 | 29 | 3303 | 519 |
| Linux race | 6 | 1329 | 0 |
| 完整 PostgreSQL | 1 | 870 | 0 |
| 原生 worker | 1 | 11 | 0 |
| 完整 HTTP／TLS／PG | 1 | 1 | 0 |

刻意把正式保存的季數加一時，HTTP驗收會失敗；逐位元復原後完整通過。Windows略過的PG與HTTP由實際執行補足。vet、產品建置、增量品牌及gitignore通過；全量品牌仍14735項違規，既有ABI差異尚未解決。見[證據](evidence/directory-nfo-sources.json)。
