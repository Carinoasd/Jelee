# 人工元資料與欄位鎖持久化

第21版遷移新增永久item的欄位狀態與版本。這一段接管理員人工編輯、欄位來源、明確鎖定／解除與實際catalog標題同步；自動TMDB／NFO写入尚待後續接線。原始媒體、NFO與圖片檔案保持唯讀。

## 正式API與欄位

啟用帳號服務後提供管理員 `GET /api/v1/items/{id}/metadata`、`PUT /api/v1/items/{id}/metadata`，不需要TMDB金鑰。一般使用者可讀的顯示欄位（不含來源識別與路徑）另由 `GET /api/v1/items/{id}/details` 提供，見 `docs/catalog-api.md`。runtime同一建構函式在有／無供應商時都綁定item repository；有供應商時保留庫偏好與圖片功能。建構回傳新服務，不變更原服務。供應商路由仍要求正式供應商，局部依賴缺失回固定503。

```json
{"expectedRevision":1,"fields":[{"field":"title","value":"人工標題","locked":true},{"field":"overview","value":""}]}
```

四個欄位為 `title`、`originalTitle`、`overview`、`date`。標題不可空白，標題／原名最多1024 UTF-8 bytes，簡介最多16384 bytes；日期為空字串或真實YYYY-MM-DD，拒絕0000年與非法閏日。值需有效UTF-8且無NUL。輸入最多四個唯一欄位、每項至少含value或locked；整個正文32KiB。未知欄位、重複欄位、JSON null、錯誤型別、未知屬性及多餘query拒400，ID採標準UUID。OpenAPI與router依帳號旗標接線。

省略value保留目前文字；省略locked保留鎖定狀態。optional欄位明確value空字串保存人工來源，與尚未提供人工值有別。鎖定欄位仍可由這個明確人工編輯入口更新文字或解除鎖定。此入口沒有自動刮削；鎖定阻止自動覆蓋的實際行為會在後续供應商寫入階段驗收。

讀取回傳itemId／libraryId／revision及有序fields，每欄含value／source／locked／updatedAt。沒有新狀態的item從原items.title投影 `existing`、版本1及空updatedAt；讀取不建立任何row。人工文字更新標為 `manual`，來源不可由客戶端偽造。只改鎖不冒充文字編輯；未存在的optional值只鎖定時以existing空值記錄。updatedAt是最後欄位狀態修改的時點，採含時區偏移的RFC3339表示，不當成上游抓取時間。回傳陣列與時間指標各自複製。

## 交易與保存

兩表以永久items.id關聯，沒有使用掃描job的臨時庫存身份。state版本範圍2–2147483647；欄位複合主鍵限制每item最多四欄，DB同樣限制欄位、來源、文字byte上限及日期。沒有對既有標題強行標記人工或供應商來源，也沒有批次改寫items。

短交易沿既有account→jobs→live actor順序，重新核對管理員session，取得item列鎖，讀目前版本並比較expectedRevision。成功全批版本加一、欄位更新、items.title同步與前後audit在同交易提交；失敗、取消與409回滾全部。讀取也沿jobs鎖，因此與這些寫入一致；此全局序列化鎖是現有架構邊界，尚未做跨item平行化或性能承諾。DB交易不外呼供應商、不等文件內容。

既有來源的標題保留原schema1的1–1024字元限制，包含合法舊空白與較長UTF-8標題；只修改鎖不截斷或改分類。此例外只屬existing來源，客戶端不能指定来源，新人工輸入維持非空白及1024bytes限制。兩類舊值均由真PG锁定後核對catalog及欄位逐字保持，再核對同值不能以新人工輸入繞過限制。

原001–020遷移不變。乾淨空狀態可降21→20，有任何保留state／field即在表鎖內拒絕降版；即使人工把文字改回原值亦拒絕。當前binary只接受schema21。新表不改既有items欄位，catalog仍按原查詢讀items.title。

## 實際驗收

真HTTP＋app＋PostgreSQL在沒有TMDB金鑰時驗證讀原標題、寫人工文字與鎖、明確清空、實際catalog API標題變更、舊版本409、明確解除保留文字、非法JSON與撤銷session不改資料。兩個同版本並行更新只有一個成功；成功稽核次數吻合。DB專項覆蓋乾淨升降版保留原標題、非管理員／取消／缺失item不建state、直接SQL約束、保留資料拒降版。注入欄位INSERT錯誤時，先前版本INSERT亦回滾且catalog原標題保持。

刻意停用正式repo的版本比較，真HTTP验收發現舊版本覆寫；finally逐位元復原後四項PG race專項通過且零略過。domain測邊界／日期／UTF-8与副本，app驗repo不能改呼叫者值／鎖指標、回傳時間隔離与無效請求不入repo，runtime測與正式建構相同函式在有／無provider均到達repo。

完整執行結果、skip逐項核對與來源SHA見[證據](evidence/item-metadata.json)。G14.6保持部分完成：自動寫入的優先序與鎖保護、可信唯讀NFO抽取接線、供應商來源／時間、清除外部元資料、worker、前端與100電影／20劇集完整矩陣尚待完成。完整品牌與ABI差異門禁保持。
