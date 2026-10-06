# 合集與播放清單（schema 81）

需求：G02.1（保留域 Collection、Playlist）、G48.3（隱藏條目不得出現在合集與 Playlist）、G07.7（刪除使用者時級聯處理其資料）。

## 資料

| 表 | 用途 |
| --- | --- |
| `collections` | 管理員維護的合集：`name`、`overview`、可選的 `nfo_name`（NFO 合集名稱，去頭尾空白、不分大小寫唯一）、`created_by`。 |
| `collection_items` | 手動加入的成員（合集 × 條目，條目刪除時級聯）。 |
| `playlists` | 使用者自己的播放清單：`owner_id`（使用者刪除或清除時級聯）、`name`、`public`。 |
| `playlist_items` | 清單項目：`position`（排序鍵，同值以 `id` 決定）、`item_id`（可重複同一條目）。 |

`item_metadata_facts` 另加部分索引 `item_metadata_facts_collection_name_idx`（`field='collection'` 的 `lower(btrim(value->>'name'))`），供 NFO 合集成員查詢。遷移 `000081_collections_playlists` 的 down 在仍有合集或播放清單時拒絕（55000），刪除後可逆；稽核紀錄保留。

## 合集

- 成員＝`collection_items` 的手動成員 ∪（設定了 `nfo_name` 時）條目 metadata 的 `collection` 事實（NFO `<set>`／`<collection>`，含手動編輯）名稱相同的所有條目。NFO 成員不複製，條目 metadata 改了就跟著變；NFO 成員不能從合集「移除」（409 conflict），要改條目的合集 metadata 或合集的 NFO 名稱。
- `POST /api/v1/collections/nfo-sync`：為每個還沒有合集使用的 NFO 合集名稱建立一個合集（名稱與簡介取該名稱字母序第一個寫法），之後帶同名的新條目自動加入，不必再同步。不會自動在掃描或 NFO 套用後執行。
- 手動成員限 Movie、Series、HomeVideo；上限：合集 10000 個、每合集手動成員 10000 個、一次加入 100 個。
- 建立、更新、刪除、加入、移除、NFO 同步都只限管理員並寫稽核：`collection.created`／`updated`／`deleted`／`items_added`／`item_removed`／`nfo_synced`。

## 播放清單

- 只有擁有者能改名、公開／取消公開、加入、移除、重新排序與刪除；其他使用者對可讀的清單得到 403，不可讀的得到 404（與不存在相同）。管理員也看不到別人的私人清單。
- 加入限 Movie、Episode、HomeVideo，依請求順序附加到尾端，可重複；上限每人 1000 個清單、每清單 5000 項。
- 重新排序：`POST /api/v1/playlists/{id}/entries/{entryId}/move`，`beforeEntryId` 為目標前一項（null＝移到尾端），整份清單重新編號為 0..n-1；看不到的項目保持原本相對位置。
- 併發：每次變更先 `SELECT … FOR UPDATE` 鎖住清單列（並鎖住操作者的使用者列），同一清單的變更依序套用，批次加入不會交錯（TestPlaylistsVisibilityAndOrderPostgres 以 8 路並行驗證）。
- 刪除使用者（軟刪除）時在同一交易刪除其播放清單，還原帳號不會帶回；使用者列被清除時 FK 也級聯。播放清單不寫稽核（屬使用者自己的資料，與偏好相同）。

## 權限過濾

每個讀取都在同一個 SQL 內以 `itemVisibleSQL` 綁定讀者（`visibility.go` 的唯一來源；參數 `requestScopeArg(ctx)`），所以：

- 隱藏條目不會出現在合集或清單內容，也不計入 `itemCount`、不會被選為 `coverItemId`；
- 非管理員只看得到至少有一個可見成員的合集；其他使用者的公開清單也要至少有一個可見項目才列出或可讀；
- 在請求本文中指名看不到的條目，與不存在的條目一樣回 404（或設定的 403）；
- 新表登記在 `visibility_guard_test.go`：只有 `collections.go`、`playlists.go`（與帳號刪除的那一句）能出現這些表名。

`internal/adapter/http/access_leak_test.go` 的全路由遍歷涵蓋合集與清單的列表與內容（含一個只有隱藏條目的合集，其名稱是不得外洩的標記），七種隱藏機制下零外洩。

## 版本合併

被合併吸收的條目的合集與清單成員列納入合併快照，撤銷時一併還原（合集或清單已刪除的除外）；合併本身不把成員資格轉給目標條目（TestCollectionPlaylistMembershipSurvivesMergeUndo）。

## 前端

`/collections`、`/collections/:collectionId`、`/lists`、`/lists/:listId`（路由與 i18n 鍵避開 play 字樣，見 no-playback 閘門），條目頁的「加入清單」區塊在點開後才載入清單；只列出與管理，從不播放。中文介面把播放清單稱為「片單」。

## 後續

- 相容層（`/compat`）目前沒有 Collections／Playlists 端點骨架，上游客戶端看不到合集與播放清單，待另行實作（BoxSet、Playlist 型別與 `/Playlists`、`/Collections` 路由）。
- metadata 備份／匯入（G36.4）尚未包含合集與播放清單。
- NFO 同步未在掃描後自動執行。
