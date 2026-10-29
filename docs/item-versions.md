# 多版本人工干預、撤銷、預設版本與音軌／字幕偏好（schema 78、99）

需求：G20.3（合併／拆分、主版本、排除誤合併、稽核）、G20.5（不跨作品誤合併、一鍵撤銷）、G20.2（預設版本依設備能力、頻寬、使用者偏好選擇）、G20.4 與 G16.5（流選擇與音軌／字幕偏好按條目、按版本保存，加上會話層級選軌記錄）。

## 資料

| 表 | 用途 |
| --- | --- |
| `item_version_operations` | 每個人工決定一列：`kind`（split／merge／primary／unexclude）、保留的條目 `item_id`、新建或被吸收的條目 `other_item_id`、移動或選定的版本 `source_ids`、撤銷所需資料 `undo`（JSON，上限 16 MiB）、`undo_until`（建立後 30 天）、`undone_at`。 |
| `item_version_exclusions` | 「此檔案不是該條目的版本」：以 root＋相對路徑記錄，來源列被刪除後依然有效。 |
| `catalog_scan_item_aliases` | 被合併條目的掃描群組改指向目標條目。 |
| `catalog_scan_sources.manual` | 管理員放置的版本；同步不再以其檔名改寫條目標題。 |
| `item_primary_versions` | 主版本：播放資訊與檔案資訊排第一，客戶端未指定版本時使用（進度回報亦同）。 |
| `user_track_preferences` | 使用者的偏好：使用者預設（無條目）、條目層（所有版本）、版本層（單一來源）。 |
| `user_version_preferences`（schema 99） | 使用者的版本偏好（G20.2）：`max_height` 取 480、576、720、1080、1440、2160、4320 之一，`dynamic_range` 取 sdr 或 hdr，另有 `prefer_smaller` 與以 bit/s 計的 `max_bitrate`；全部可為 null、至少一項非 null，空白即刪列。 |
| `session_track_selections`（schema 99） | 會話選軌（G20.4）：每（使用者，條目）一列，記最後一次播放會話（`play_key`）在哪個版本（`source_id`）切到的 `audio_track`／`subtitle_track`（`e:<索引>`、`x:<外掛軌 ID>`，字幕另可 `off`）。 |

所有操作與 catalog_sync、匯入、探測同樣持有 jobs 鎖，因此與同步批次互斥。

## API（管理員；細節以 OpenAPI 為準）

- `GET /api/v1/items/{id}/versions`：主版本、排除清單（只回檔名）、最近 50 筆操作與可否撤銷（`undoable`／`undoBlocked`）。
- `POST /api/v1/items/{id}/versions/split` `{"sourceId","exclude","title"}` → 201。建立同類型、同媒體庫、同父項的新條目（標題以 `manual` 來源寫入），移動該版本的來源、掃描登記（標記 manual）與版本層偏好；`exclude` 另記排除。唯一的版本不能拆分（409 `version_merge_incompatible`）。播放紀錄留在原條目（見一致性檢查）。
- `POST /api/v1/items/{id}/versions/merge` `{"sourceItemId"}` → 201。把來源條目的所有版本與播放工作階段移入路徑條目，依下列策略轉移後刪除來源條目。
- `PUT /api/v1/items/{id}/versions/primary` `{"sourceId": uuid|null}`；未改變為 409 `conflict`。
- `DELETE /api/v1/items/{id}/versions/exclusions/{exclusionId}`：解除排除。
- `POST /api/v1/version-operations/{id}/undo`：撤銷。

非管理員一律 403（查詢前即拒絕）；管理員受 `restrict_admins` 等內容規則限制時，看不到的條目與不存在的條目同為 404，且不會被修改。稽核事件：`item.version_split`、`item.versions_merged`、`item.primary_version_changed`、`item.version_exclusion_removed`、`item.version_operation_undone`。Webhook（G12.1，開啟時）：拆分建立的條目發 `media.added`、合併刪除的條目發 `media.deleted`；撤銷時反過來，與決定在同一交易。

## 合併的邊界（G20.5）

以下情況一律拒絕，**沒有 `--force`**：外部 ID 是事實，衝突代表兩個條目真的是不同作品，強制合併只會產生錯誤的條目；正確的修法是先修正中繼資料。

- 409 `version_merge_incompatible`：不同媒體庫、不同類型（電影 vs 單集）、容器條目（Series／Season）、來源條目還有子項、單集的所屬劇集不同（以季→劇集祖先比較；一個有父項一個沒有也算不同）。
- 409 `version_identity_conflict`：兩者的 `uniqueIds` 在同一提供者（不分大小寫）上沒有任何共同值；或 `seasonNumber`／`episodeNumber` facts、掃描解析的季／集數兩邊都有且不同。
- 409 `version_item_busy`：任一條目正在播放（active 工作階段）、有以它為目標的探測工作紀錄（`probe_requests`／`probe_job_state` 以 RESTRICT 指向條目），或有排隊中／執行中的 NFO 寫入。

## 合併的轉移策略

- 版本：`media_sources`、`catalog_scan_sources`（標記 manual）、版本層偏好隨來源移動；探測快取只解除與條目的連結（`item_id=NULL`），結果本身仍描述同一檔案。
- 播放工作階段（歷史與統計標記）移到目標。
- 使用者資料：任一邊已播放即為已播放；播放次數相加；續播點與最後版本取較晚播放的一邊。
- 存取規則：任一邊 `hide` 即 `hide`，否則 `allow`（不因合併讓受限使用者看到原本看不到的檔案）。注意：被吸收條目的分級、標籤不會帶過去，合併後以目標條目的中繼資料判定可見性。
- 觀看統計：每使用者每日列相加；歷史列的次數相加。
- 條目層偏好：目標沒有時複製。
- 掃描群組：被吸收條目的群組與指向它的別名都改指向目標。
- 被吸收條目本身的列（條目、中繼資料、facts、NFO 鎖與觀測、目錄登記、父項連結、掃描群組、圖片列、使用者資料、規則、統計、條目層偏好、排除、主版本）完整保存在 `undo` 中；`TestItemVersionSnapshotCoversCascades` 確保任何從 items 級聯刪除的新表都必須被保存、移動或明確列為可捨棄（目前只有會過期的 `nfo_write_preparations`）。

## 撤銷（G20.5）

- 時效：建立後 30 天；已撤銷不能再撤銷。
- 順序：split／merge 只能由新到舊撤銷——同一批條目上有較新的、未撤銷的 split／merge 時回 409 `version_undo_unavailable`（`undoBlocked=later_operation`）。primary／unexclude 不參與這個順序，也不阻擋結構撤銷。
- 撤銷合併：以原 ID 重建被吸收條目與其所有列，把仍在目標上的版本與工作階段移回，刪除合併建立的別名並把轉移過來的別名指回。目標上的使用者資料、偏好只有在合併後未再變動時（`updated_at` 等於合併時刻）才還原；規則只有仍是合併結果時才還原；統計只在每個計數都還足夠時才扣回。合併後才產生的播放與變更保留在目標上。
- 撤銷拆分：把版本移回原條目，新條目在拆分後累積的工作階段、使用者資料、規則、統計依合併策略併回原條目，再刪除新條目與排除；若新條目上還有別人放進去的版本則拒絕。新條目已不存在時只解除排除。
- 撤銷主版本：恢復先前的主版本；該版本已不屬於此條目時改為沒有主版本。撤銷解除排除：重新加回排除。

## 與掃描同步的互動

人工決定不會被下一次同步還原（`TestItemVersionDecisionsSurviveCatalogSyncPostgres`，真實掃描＋同步）：

- 已登記的來源同步本來就不搬動；manual 來源的檔名不再改寫條目標題。
- 新檔案（包括消失後重新出現的檔案）分組時：群組指向被排除的條目時，與「另有 NFO 身分」的檔案相同改用單檔群組，拆分時已為新條目登記這個單檔群組，所以檔案回到新條目；群組已被合併時經別名回到目標條目。
- 測試以解除排除作為對照：解除後同一檔案重新出現即回到原群組。

明確匯入的來源不屬同步管理，本來就不會被同步搬動。

## 一致性檢查

`version_count` 的使用者資料與工作階段兩段：`last_source_id`／`source_id` 指向「被未撤銷的拆分從此條目移出的版本」時視為正常（原條目保留它的播放紀錄），修正動作同樣不清除它們。

## 預設版本選擇（G20.2）

播放資訊的來源順序就是預設版本順序，依序套用：

1. **客戶端能力聲明**（只用來排除）：自有 API 的 `POST /api/v1/items/{id}/playback/check`（`containers`、`videoCodecs`、`audioCodecs`、`maxBitrate`，判定同直投 `DecideDirectPlay`，未探測來源不確認）、相容層 PlaybackInfo 的 `DeviceProfile.DirectPlayProfiles`、`MaxStreamingBitrate`、`EnableDirectPlay`、`MediaSourceId`。不能原樣直投的版本被排除；聲明**永遠不會**讓轉換變成可接受（ADR 0007，守衛照舊 409 轉換參數），全部被排除時照舊：自有 API 200 且 `directPlayable=false`、相容層 `NoCompatibleStream`。
2. **頻寬上限**（使用者偏好 `maxBitrate`）：已知位元率不超過上限的版本在前，超過的依位元率由低到高；未知位元率不算超過。客戶端自己聲明的上限屬第 1 步（硬排除），使用者偏好的上限只排序、不隱藏。
3. **其他使用者偏好**，依序：解析度上限 `maxHeight`（以解析度級別比較，1080 包含 1920×800 的寬銀幕；不超過者在前，超過者小的在前）、動態範圍 `dynamicRange`（hdr 涵蓋 HDR10／HDR10+／HLG／杜比視界）、`preferSmaller`（檔案小的在前，大小未知排最後）。
4. 既有規則：主版本 → 品質分數 → 來源 ID。

偏好存在 `GET/PUT /api/v1/users/me/version-preferences`（本人，不稽核；null 或省略的欄位不設規則，整個空白即刪除），元資料備份匯出為 `user_version_preference`（以較晚更新的一邊為準，分享訪客不匯出）。

可解釋性：自有播放資訊與檢查回應帶 `selection`：`defaultSourceId`（沒有可播放版本時省略）、`reason`（使預設版本勝過其他可播放版本的最高優先規則：`only_version`、`only_playable`、`bandwidth_preference`、`resolution_preference`、`dynamic_range_preference`、`size_preference`、`primary_version`、`quality_score`、`stable_order`；沒有可播放的為 `none_playable`，沒有版本為 `no_version`）、`preferences`（已設定的偏好規則）、`excluded`（被聲明排除的版本與直投理由碼）。相容層回應維持上游形狀，第一個 `MediaSource` 即預設版本，理由碼寫在 debug 日誌 `default version chosen`（只有來源 ID 與固定代碼）。

同一順序也用於相容層串流未指定 `MediaSourceId` 時選的「最佳版本」。未指定版本的播放開始回報（記錄 `last_source_id`）仍以主版本、品質分數決定，因為回報時不讀偏好。檔案資訊頁（條目詳情）維持主版本、品質分數順序。

## 會話層級選軌（G20.4）

- 記錄：播放回報帶目前的音軌／字幕時——相容層 `AudioStreamIndex`／`SubtitleStreamIndex`（`-1` 關閉字幕；外掛字幕依上游編號，接在所有內嵌串流之後，與 PlaybackInfo 相同的 `domain.UpstreamExternalSubtitles`），自有 API `audioTrack`／`subtitleTrack`（`e:<索引>`、`x:<外掛軌 ID>`、字幕 `off`）——與該會話上次記錄不同才寫入（緩衝在記憶體記下指紋，不同步每次進度），對照會話正在播放的版本解析，版本沒有的軌道略過；寫入失敗只記警告、下次回報重試，不影響回報本身。
- 儲存：每（使用者，條目）一列，最新的會話覆蓋舊的；同一條目在兩台裝置同時播放時以後寫者為準。寫入與工作階段、可見性綁在同一語句，看不到的條目不寫。
- 套用：該使用者之後取播放資訊（續播、同會話重新取 PlaybackInfo、換裝置）時，只對記錄的那個版本套用，`basis` 為 `session`；換到其他版本不套用（串流索引只對原版本有意義）。
- 優先序（最具體者先）：**會話選軌（同版本）> 版本層偏好 > 條目層偏好 > 使用者預設（G16.5）> 帳號語言（字幕）> 檔案預設旗標**。會話選了某字幕時即使較寬層級設 `off` 也顯示；會話關閉字幕時即使較寬層級設 `always` 也不顯示；只回報音軌時字幕仍照偏好。
- 失效與清理：30 天後讀取忽略；每次寫入順帶刪除最多 100 列全站過期資料與該使用者超過 1000 列的最舊資料；儲存該條目的偏好（`PUT …/track-preferences`）刪除該條目的會話選軌，儲存使用者預設刪除該使用者全部會話選軌（刻意的偏好不被較舊的切換蓋過）；清除播放紀錄（`DELETE /api/v1/users/me/playback-history`）一併刪除；條目、版本、使用者刪除時級聯刪除；版本被拆分到別的條目後不再套用。合併／拆分快照不保存它（`versionSnapshotDropped`），元資料備份不匯出（觀測資料）。
- 不稽核，與偏好相同；不轉碼、不改投遞，客戶端仍可自由切換。

## 音軌／字幕偏好（G16.5、G20.4）

- `GET/PUT /api/v1/items/{id}/track-preferences`（任何使用者，只讀寫自己的；看不到的條目與不存在相同 404；版本必須是該條目目前的版本）。`PUT` 取代一層：帶 `sourceId` 為版本層，否則為條目層；null 或省略的欄位繼承上一層；整個空白即刪除該層。
- `GET/PUT /api/v1/users/me/track-preferences`：使用者預設，不能指定軌道。
- 欄位：`audioLanguage`、`audioCommentary`、`audioTrack`、`subtitleMode`（auto／always／forced／off）、`subtitleLanguage`、`subtitleSdh`、`subtitleTrack`。語言接受 ISO 639-1／639-2 與 BCP 47（`eng`→`en`、`chs`→`zh-Hans`）；軌道為 `e:<串流索引>` 或 `x:<外掛軌 ID>`，只能存在版本層。
- 合併順序：會話選軌（同版本，見上節）→ 版本 → 條目 → 使用者 →（字幕語言）帳號語言 `users.locale` → 檔案本身的預設旗標。`basis` 回報做決定的最具體層級（`session`、`version`、`item`、`user`、`locale`、`source`）。
- 選軌：指定軌道存在就用；音軌依「明確要求評論音軌」→語言（同標籤 > 同語言同書寫系統，如 zh-TW≈zh-Hant > 同主語言）→避開評論音軌→預設旗標；字幕 auto 在音軌語言與字幕語言不同時選完整字幕（強制字幕不算），否則只選強制字幕；off 不選；沒有任何語言時沿用檔案預設字幕。
- 自有播放資訊 `GET /api/v1/items/{id}/playback` 的每個來源帶 `defaultTracks {audio, subtitle, basis}`；相容層 PlaybackInfo 填 `DefaultAudioStreamIndex`（偏好選到內嵌音軌時）與 `DefaultSubtitleStreamIndex`（內嵌索引、外掛字幕的上游編號，或 -1 表示不顯示字幕；外掛字幕需直投開啟），直投判定也改以偏好的音軌編碼檢查。偏好不轉碼、不混音、不燒錄，客戶端仍可自由切換。
- 偏好不寫稽核（與介面偏好相同，只影響起始軌道）。

## 備份、降級

- 元資料備份新增 `catalog_scan_item_alias`、`item_version_exclusion`、`item_primary_version`、`user_track_preference`，`catalog_scan_source` 帶 `manual`。人工決定是管理員選擇，匯入時對目標中已存在的條目也寫入（目標已有自己的決定則保留）；偏好與進度相同，以較晚更新的一邊為準。操作紀錄只為時效內撤銷服務，與稽核、工作同樣不匯出。
- 降級（099 down）：直接刪除版本偏好與會話選軌兩張表；schema 98 退回主版本、品質分數與既有偏好。
- 降級（078 down）：仍有排除或別名時拒絕（schema 77 的同步會默默把拆出的檔案歸回、重建被合併的條目），請先解除排除並撤銷合併；偏好、主版本與操作紀錄直接移除。已完成的拆分、合併本身是一般目錄資料，降級後保留。

## 前端

條目詳情頁的「檔案資訊」區塊下：管理員看到版本區塊（設為主版本、拆分＋排除、以條目 ID 合併、排除清單、變更紀錄與撤銷，全部兩段確認，錯誤依代碼顯示四語訊息）；所有使用者看到音軌與字幕偏好表單（只是偏好設定，沒有任何播放元件）。兩者是懶載入的 chunk，四語文字也放在懶載入的 i18n 命名空間 `versions`，不計入初始 bundle。

## 驗證

真 PG：`TestItemVersionSplitMergePrimaryAndUndoPostgres`、`TestItemVersionMergeBoundariesPostgres`、`TestItemVersionPermissionsPostgres`、`TestItemVersionDecisionsSurviveCatalogSyncPostgres`、`TestTrackPreferencesPickDefaultTracksPostgres`、`TestItemVersionsMigrationRoundTrip`、`TestItemVersionSnapshotCoversCascades`、`TestVersionsAndTrackPreferencesHTTPPostgres`（自有 API 與相容層 PlaybackInfo）、`TestAccessLeakHiddenContentPostgres`、`TestMetadataBackupDrillPostgres`。單元：`TestSelectDefaultTracks` 等 domain、app、HTTP、compat 測試與前端 `features/items/versions.test.ts`。

G20.2／G20.4：單元 `TestSelectVersionRules`、`TestSelectVersionNeverAcceptsConversion`、`TestNormalizeVersionPreference`、`TestResolveTrackReport`、`TestSessionChoicePrecedence`、`TestCatalogPlaybackChoiceOrdersByPreference`、`TestCatalogRecordsSessionTracks`；真 PG `TestPlaybackSelectionHTTPPostgres`（偏好排序、聲明只排除、相容層與自有 API 記錄與套用、過期、偏好與清除紀錄使其失效）、`TestPlaybackSelectionMigrationRoundTrip`、`TestMetadataBackupDrillPostgres`。

未涵蓋：前端尚無版本偏好表單（只有 API）；真實客戶端對 `DefaultSubtitleStreamIndex`／`defaultTracks`、`AudioStreamIndex`／`SubtitleStreamIndex` 回報與 `MediaSources` 順序的實際行為（擁有者驗證 B13、C31、D31）、大量版本／大量歷史條目的合併耗時與撤銷文件大小、外掛音軌在相容層沒有串流索引（偏好選到外掛音軌時 `DefaultAudioStreamIndex` 退回檔案預設）。
