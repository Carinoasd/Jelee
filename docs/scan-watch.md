# 目錄監看與掃描

目錄監看將本機檔案變動轉成既有 `inventory_scan` 任務。設定與待處理狀態保存於 PostgreSQL；服務重啟會重新核對目錄。預設關閉，不修改原始媒體、NFO 或圖片。

## 管理 API

透過既有 `PUT /api/v1/libraries/{id}/schedule` 完整更新定義，新增 `watch` 布林欄位。`enabled` 控制定時掃描，`watch` 控制檔案監看，兩者可以獨立啟停。更新必須帶目前 `expectedRevision`，第一次建立用零；仍須提供有效的排程時間設定。

```json
{
  "expectedRevision": 0,
  "enabled": false,
  "watch": true,
  "timing": {
    "mode": "interval",
    "intervalSeconds": 3600,
    "cron": "",
    "timezone": "Asia/Taipei"
  },
  "probe": false,
  "nfo": false,
  "ignore": {"mode": "", "caseMode": ""}
}
```

這份定義只啟用監看，事件建立的工作沿用其中的 probe、NFO 與忽略設定。定時掃描契約見 [持久掃描排程](scan-schedules.md)。更新完整定義時須保留希望繼續啟用的 `watch` 值。

管理員可讀取 `GET /api/v1/libraries/{id}/watch`，取得 `enabled`、`observing`、`pending`、`lastJobId` 與 `lastError`；尚未建立排程定義時回傳 404。API 不公開路徑或租約識別資料。`observing=true` 表示目前租約與設定有效，且觀察器已完成初次安裝。

`pending` 表示仍有變更尚未提交為掃描任務；它不表示掃描成功。已提交工作的結果與重試使用既有任務 API，由 `lastJobId` 查詢。工作失敗不會自動把已接受的事件重新標成待提交。

## 合併事件與復原

- 一般事件等待一秒安靜期；持續事件最多等待五秒便標記待處理。
- 新增、移除或改名等目錄結構變動，兩秒後重建目錄觀察集合。每五秒檢查根目錄物件是否仍一致。
- 啟動與每次重建均要求一次完整清單核對；事件提供的檔名不直接拿來開啟檔案。
- 管理迴圈每秒運行，使用三十秒租約、每十秒續租、每五秒嘗試提交。資料庫操作有兩秒期限。
- 同庫忙碌、佇列滿或所需能力不可用時，保留待處理世代並至少等待三十秒重試。觀察器失效也延後三十秒重建。

多實例透過資料庫租約協調。租約持有者、租約世代、設定版號、媒體庫根目錄世代與資料庫到期時間均須吻合；舊觀察器不能繼續寫入。建立工作與接受事件世代在同一筆交易完成，交易中租約到期則回滾。提交期間新到的事件保留給下一次掃描。

停權、刪除或降為非管理員的擁有者不能繼續觸發監看工作；登出不取消已保存的管理意圖。有效管理員可更新定義重新接管。停用監看不取消已建立的任務。

## 支援環境與資源上限

目前驗證本機 Linux 與 Windows。Linux 使用固定版本 [fsnotify v1.10.1](https://github.com/fsnotify/fsnotify/tree/v1.10.1)，透過 `/proc/self/fd` 註冊已安全開啟的目錄，需有可用的 `/proc`。Windows 使用已開啟目錄的相對控制代碼、檔案身分比對與非同步 IOCP 通知，避免重新依原始字串路徑開啟目錄。

Windows 通知依 [ReadDirectoryChangesW](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-readdirectorychangesw) 處理，通知溢位時重新列舉；相對控制代碼開啟使用 [NtCreateFile](https://learn.microsoft.com/en-us/windows/win32/api/winternl/nf-winternl-ntcreatefile)。控制代碼被改名後仍追蹤原物件的行為，另有實際 Windows 測試。

全域最多啟用八個媒體庫，每庫一至三十二個根目錄；預設每個觀察器最多一千零二十四個目錄。超出限制回報 `resource_limit`。不追蹤符號連結或 Windows reparse point。Linux 已知 NFS、SMB／CIFS、FUSE 與 9P 類型，以及 Windows UNC 根目錄，回報不可用；其他平台尚未提供觀察器。遠端檔案系統的通知限制亦見 [fsnotify 說明](https://github.com/fsnotify/fsnotify#faq)。

觀察器不可用時不暗中改成持續列舉；需要定期核對時，可另外啟用既有定時掃描。安全錯誤碼為 `observer_unavailable`、`resource_limit`、`admission_unavailable` 或 `owner_unavailable`。

## 遷移與驗收邊界

schema42 新增監看狀態與設定，001–041 的八十二份 SQL 保持原樣。保留監看設定或狀態時拒絕降版，避免遺失待處理事件。

這部分仍屬第三階段。短時間事件突發、雙實例與重啟測試不能替代大規模負載曲線或二十四小時穩態驗收；前端監看設定也尚未交付。

## 驗證

| 驗證 | 套件 | 通過事件（含父測試） | 略過 |
| --- | ---: | ---: | ---: |
| Windows 全套 | 30 | 3327 | 561 |
| Linux race | 8 | 1334 | 0 |
| 完整 PostgreSQL race | 1 | 912 | 0 |
| 原生 worker | 1 | 11 | 0 |
| 完整 HTTP／TLS／PG | 1 | 1 | 0 |

[機器可讀證據](evidence/scan-watch.json)。真實 PostgreSQL 覆蓋雙實例搶租約、過期與交易中到期回滾、忙碌事件保留、設定與根目錄失效、啟用上限、201 檔突發、實際 worker 重啟與降版保護。兩平台檔案通知測試另驗證新子目錄重建、改名後控制代碼身分、取消與資源上限；TLS 管理 API 覆蓋權限及安全回應。Windows 的 PG／原生限定略過由 Linux 專項補足。
