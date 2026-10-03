# NFO 準備的 root generation（schema52）

本階段補上資料庫 root generation，原生 root／media／ancestor 的持久準備證據仍須實作。

## 保存與使用

`ResolveItemNFO` 在同一短交易鎖定來源與 root，讀取 `library_roots.nfo_generation`。準備保存它，job-owned entry 複製同一觀察；準備 TTL 清除不回填或刪除工作意圖。新準備與內部 prepared writer／Stage 拒絕缺少觀察的資料。

schema52 為 preparation 與 entry 新增 nullable `root_generation`。NULL 表示歷史資料沒有這項觀察；升版不以當下 root generation 回填。歷史資料仍可透過受授權的觀察 port 讀取，回傳的 domain 值為零；新保存、意圖複製、Begin、plan／ready 首次與重放拒絕缺少證據。歷史 schema 的讀取先查欄位是否存在，不將整筆 XML payload 轉成 JSON 判斷資料形狀。

SQL insert／no-op update 鎖定 root，核對 library、root ID、generation 與 root path，deferred trigger 再核提交邊界。schema51 的 catalog 函式保留為 v51，原呼叫名稱增加 root generation 複核，原有 journal／plan／ready 與 catalog 變更端守衛繼續使用同一邊界。

root 變更端檢查本交易寫入／重放的 preparation、entry，涵蓋提前 `SET CONSTRAINTS`、active／released 子交易。generation、path、ID 變更與刪除皆須重新核對；已回滾的證據不凍結後續正常編輯。generation update 不得倒退，避免將舊數值重新用於現有 root。這不構成資料庫 UUID 與原生物件身分的等價證明。

Save 先取得全域 preparation quota fence，再解析並鎖定當下 scope，與 SQL insert 的鎖順序一致。真 PG 負例證明舊順序會使等待容量的 Save 先持有 root，阻擋容量鎖持有者取得該 root；修正須讓 Save 等待時沒有持有 root，解除等待後正常保存。

任何新 root observation 或 journal 留存時，schema52 降版拒絕並保留 dirty51 與資料；空降升保留 metrics epoch。001–051 的 102 份已發布 SQL 保持原文。

## 驗證邊界

980 份 Go／SQL 來源凍結，完整 PostgreSQL race 459 根四分片各執行／通過一次，共 1454 PASS，零 skip／fail；其中 root generation 十根 54 PASS。Windows 七套件 1043 PASS／989 條件 skip／零 fail，未證 Windows 真 PostgreSQL。metrics runtime 真 PostgreSQL race 三根 8 PASS、Linux telemetry race 78 PASS，兩平台 vet、Darwin NFO 僅編譯、格式及增量品牌檢查通過。見[完整安全證據](evidence/nfo-write-root-generation.json)與[指標契約](evidence/runtime-metrics-contract.json)。

驗證涵蓋應用與直接 SQL 的 journal／plan／ready 首次及重放、首次 stage／ready 重開的剩餘 bytes、準備保存與 job copy、三種隔離層級、SAVEPOINT／提早 flush、root path／ID／刪除及 counter 倒退、歷史缺證與降版保留。

舊 schema46／47／48 的 migration 測試在空資料庫先選定歷史版本，再用 owned SQL fixture 建立原始資料形狀。這些 fixture 不為正式 write port 增加舊版降級路徑。容量測試為合成 SQL storage 案例，各 library 使用自己的唯一 root row；它們不是原生媒體授權或完整 heap／RSS 證據。

仍缺原生 root／media／ancestor 跨程序準備觀察、同實體未解決排除、partial stage 處置、backup／target Rename／rollback／結算／crash 恢復、Windows directory metadata 耐久性、正式 worker／API／CLI 與三種批次操作。read-write runtime 保持關閉。原 24h 圖片長測來源不包含本階段。
