# 合併忽略模式的多輪重掃

正式 app 准入、PostgreSQL、同一原生 scanner 與正式 worker 連續執行六輪掃描。自有規則切換 include／exclude，舊規則改文，再移除兩個規則檔。固定檔案 mtime，包含內容長度相同的規則變更，驗證快取不沿用舊決定。

每輪核對來源報告的家族、規則行與相對路徑。初次排除不建立基線；歷史排除保留原 observed_revision 與大小；包含項目更新 revision。所有媒體內容保持原值，每輪結束 helper Active=0。

## 移除規則檔與覆核

兩個規則檔也在原掃描基線內。刪除後 Missing=2，沒有媒體誤報缺失。四筆基線中兩筆缺失，達到測試設定的 50% 門檻時 ReviewRequired=true，媒體 revision 與兩個規則檔基線全部保留。另一個使用 100% 門檻的獨立情境不觸發覆核，正常更新媒體並移除兩個規則檔記錄。正式門檻與發布實作未修改。

首輪錯誤期待規則檔移除不觸發覆核，結果 1 pass／1 fail。確認既有門檻以大於或等於比例判斷後，補齊兩條驗收路徑；失敗日誌保留於本機，未覆寫。

## 證據

[原生 PostgreSQL worker 回歸](evidence/ignore-family-rescan.json)：7 項頂層通過、零失敗／略過，26.833 秒，sourceUnchanged=true；包含本次兩組六輪重掃、原生儲存、發布、NFO 排除、恢復、來源變更與 unknown。前一輪亦含既有單一規則模式重掃，2 項頂層通過，25.784 秒。Windows PostgreSQL 套件編譯、可執行單元測試與 vet 通過；該環境沒有真實 PostgreSQL，不作整合測試聲明。

G22 仍待其他舊格式、取消／租約失效、混合 probe／NFO／圖片、規模與穩定性驗收。
