# 指定影片匯入種類

`jelee-cli import-video` 可用 `--kind` 明確指定 `HomeVideo`、`Movie` 或 `Episode`。省略時仍為 `HomeVideo`；值區分大小寫。影集與季屬於資料夾條目，不接受 `Series` 或 `Season`。

```sh
jelee-cli import-video --library "影集" --root /media/series \
  --file "Example/Season 01/Example.S01E01.mkv" \
  --title "第一集" --kind Episode
```

使用既有資料庫設定及本機管理入口。只登記既有、根目錄內的普通影片檔；不修改原媒體、NFO 或圖片，不自動刮削或讀取 NFO。重複路徑失敗時，條目、來源與稽核一併回滾。

登記後可在啟用唯讀 NFO 的媒體庫中明確確認套用同名 NFO。Episode 保持單集種類；HomeVideo 確認電影 NFO 後依既有行為分類為 Movie。

本段沿用第 38 版資料表，不新增 SQL。匯入登記不代表已完成 ffprobe、全庫掃描匯入、影集／季父子關聯或前端流程。

## 驗證

正式 CLI、真實檔案與隔離 PostgreSQL 驗證通過：四種指定／預設路徑、匯入後 NFO 套用、重複匯入原子性及非法種類拒絕。

| 驗證 | 套件 | 通過事件（含父測試） | 略過 |
| --- | ---: | ---: | ---: |
| Windows 全套 | 29 | 3298 | 514 |
| Linux CLI／domain race | 2 | 483 | 0 |
| 相關 PostgreSQL race | 1 | 38 | 0 |

vet、產品建置、增量品牌與 gitignore 通過；全量品牌仍有14735項既有違規。沒有資料表變更，相關資料庫回歸涵蓋匯入、目錄、帳號及單集NFO。見[證據](evidence/import-video-kinds.json)。
