# NFO ready 證據讀取與重開核對

`GetNFOWriteCommitFiles` 是內部、有界的觀察 port。它只讀取既有 journal token 所屬紀錄及可選的 plan／ready，不建立 journal、不刪除或結算。短交易核真實 running nfo_write 工作、owner／generation／租約、取消及活躍管理員；鎖定工作與 actor，取資料後再核租約，失敗只回零值。歷史 journal 時間保持，不能代替現在的租約。

結果包含私有 record、PlanRecorded／ReadyRecorded 及固定大小身分；沒有 JSON 公開欄位，格式化遮蔽。保存時的 SQL 守衛及不可變資料契約仍保持；讀取只是當下觀察，不能授權未來的 target Rename。缺 journal 明確失敗；有 plan 但無 ready，回傳這個狀態，不猜已完成。

## StageCommitFiles 的 ready 重開路徑

owner 持原始 payload 保留及讀取 I/O 准入，取得工作所屬意圖和證據，再按保存 recipe／固定 UUID 受控重建。若 ready 已保存，持共用 I/O 及 native target 鎖，核目前 Source／parent／target、原文與所有五個保留名稱的完整 bytes／原生身分；target 必須仍是原文實體。

核對成功後，重放相同 plan／ready 保存 port，讓既有短交易在提交時重核 owner／generation／租約／取消／actor；再核 Source、native 鎖及全部檔案。成功只表示準備檔案仍吻合，沒有 Rename、備份輪替、刪除、journal 解決或工作成功終態。ready 回應遺失但資料庫已提交時，可用同 token 從資料庫重讀後走這條路徑。

output 或 pin 缺失、rollback 換成相同 bytes 的新實體、parent 更換、target 已等於輸出或權限／租約失效均拒絕，保留剩餘證據。未保存 ready 的部分 stage 仍走原 EXCL 準備契約，碰撞保持；目前不能自動辨識或重建這些未知物件。其他 Writer 在 ready 保存前已讀取舊狀態時，仍可能需重新觀察再重試。

## 驗證與剩餘範圍

Windows／Linux owned 夾具模擬 ready 成功後回應遺失，使用新 Source／Writer 重開，確認 plan／ready 租約 port 再次執行；缺失證據、換實體、相同輸出 bytes 與錯誤 plan 拒絕。真 PG 測試核 plan-only／ready 讀取、missing 不建立 journal、owner／generation／expiry／cancel／disabled／not-admin／token／sequence／job 拒絕且不回部分資料。

真 Linux PG 夾具完成 stage、刪除準備 TTL 資料後，子程序使用新的 lease 讀取 port 與 Writer 實際重開 ready；另測實際 DB commit 後遺失回應的重試及原生更換／租約負例，前後完整剩餘檔案保持。測試數字、來源及範圍見[安全證據](evidence/nfo-ready-resume.json)。本批無新 SQL、不當完整PG回歸重跑；Windows真PG連線仍未驗證，Darwin只有交叉編譯。

完整跨程序 root／媒體／catalog revision／policy generation 證據、同實體未解決工作排除、target 提交邊界、backup／rollback／結算與 crash 恢復仍缺。部分 stage 沒有自動續作；原始 payload 預留也不是完整 heap／RSS 配額。Windows directory metadata 斷電耐久性仍未證明，runtime 沒有啟用正式寫回；全G00–G51尚未完成。
