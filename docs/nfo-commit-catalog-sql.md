# NFO 提交證據的 SQL catalog 守衛（schema51）

固定 entry 是保存的工作意圖，不能授權過期 catalog。新的 migration51 在 journal、plan、ready 的 INSERT 及 no-op UPDATE 前重新核對 item/library/source/root、kind、metadata revision、library policy generation，以及原先的 root、media 或 directory 路徑。資料列鎖保留到交易結束，deferred trigger 再驗同一範圍。

## 提早驗證與舊快照

只加 deferred trigger 不夠。交易能先 SET CONSTRAINTS ALL IMMEDIATE，之後才改 catalog。本批已實際重現 journal、plan、ready 及各自 replay 六階段繞過。新的 catalog mutation trigger 從 journal/plan/ready 的 xmin 判定本交易剛保存或重放的證據，再核受影響 item、library 或 root。過去交易留下的證據仍可讀取，不阻止正常後續編輯。

來源或 revision 改變也必須更新被鎖 item 的 MVCC 版本，避免 Repeatable Read 或 Serializable 仍讀舊的唯一來源或 revision1 無 state row。media 插入、刪除與 path 變更沿用已發布的 probe_mapping_changed；新增 source ID、directory source、metadata state 的 touch。隔離 overlay 停 metadata touch，真 PG 的 RR 直接 revision 變更負例確實失敗。停新 media touch 的早期試驗仍通過，說明該操作已由 probe 守衛涵蓋，因此移除重複工作。

## 驗證與保留

直接 SQL 矩陣驗六階段、九種 scope 變更；三種隔離層級驗来源歧義、刪除與直接 SQL revision。另驗 24 個提早驗證後修改 catalog 的案例、deferred 守衛隔離測試、空 schema50→51 降升、retained journal 拒絕降版且 dirty50、升級後歷史 scope 可觀察但不能保存新的 plan。合成身分僅驗資料庫，不是原生檔案授權。

初版只用 deferred 的紅測保留。完整守衛使六個 deferred 夾具在 UPDATE 當下被拒絕；修正 owned 夾具以單獨驗 deferred，沒有削弱正式 migration。舊445根完整PG race分片已各通過一次、零skipfail；之後補充稽核重現12個SAVEPOINT及Series來源切換缺口。修正原型26PASS，正式來源相同26個事件亦PASS／零skipfail。修正後449根完整PG race四分片皆exit0，每根各run／pass一次，合計1400PASS／零skipfail。975份Go／SQL來源雜湊已逐一核對，Windows兩套件234PASS／930DB條件skip；vet兩平台、Darwin NFO僅compile、格式／增量品牌0／339／gitignore／diff通過。方法與範圍見[安全證據](evidence/nfo-commit-catalog-sql.json)，本批隨提交沿既有PR發布。

降版在任何 journal 存在時拒絕，保留未解決證據；空降升不重設 metrics epoch。100 份已發布 SQL 保持，schema51 只增加新檔案。

## SAVEPOINT 與 Series 來源切換

xmin 不一定是頂層交易ID。SAVEPOINT 內新建或重放證據後，即使 RELEASE，再提早驗證 constraints 並修改 catalog，也必須拒絕。新 helper 從可見 xmin 的 epoch 與近期交易狀態辨識本交易未提交資料，涵蓋子交易；不把歷史證據當本次活動，也不讓已回滾的證據凍結正常 catalog 編輯。原型及正式選測驗 active、released、ROLLBACK TO SAVEPOINT 與72個子交易。未實際跑至XID wraparound。

Series 原先只有media source時能選adjacent NFO；新增directory source後原意圖不再可執行。SQL守衛現在比照應用resolver拒絕這種切換。這是catalog資料比較，仍不能代替原生root／媒體實體證据。

交易身分與狀態語意參考[PostgreSQL子交易](https://www.postgresql.org/docs/16/subxacts.html)及[系統資訊函數](https://www.postgresql.org/docs/15/functions-info.html)。

## 後續提交邊界

root generation 尚未獨立保存；root/media/ancestor 的原生跨程序證據、同實體未解決排除、target Rename、backup、rollback、結算及 crash 恢復仍缺。這些守衛只保護資料庫準備證據，不能啟用正式 read-write 或 worker。Windows 真 PG 與 directory metadata 耐久性也未證。正式24h來源24caf不含候選51。
