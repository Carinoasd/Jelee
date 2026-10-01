# 受控程序 runner

`internal/platform/process` 提供程序生命週期管理。它尚未接入 HTTP、背景掃描或媒體探測；`probe` 能力仍關閉。這個套件不會自行下載工具，也不會把 FFmpeg 加入正式容器。

## 介面與信任邊界

```go
runner, err := process.New(process.Config{
    MaxConcurrent: 2,
    Timeout: 15 * time.Second,
    MaxStdoutBytes: 1 << 20,
    MaxStderrBytes: 64 << 10,
    TempRoot: projectOwnedTemporaryDirectory,
}, []process.Tool{{
    ID: "ffprobe",
    Path: verifiedAbsoluteExecutablePath,
    Operations: map[string][]string{
        "version": {"-version"},
    },
}})
result, err := runner.Run(ctx, process.Request{
    Tool: "ffprobe",
    Operation: "version",
})
```

正式 `New` 只接受 `ffprobe` ID 與 `ffprobe`／Windows `ffprobe.exe` 檔名。工具註冊層負責驗證版本、checksum、來源及安裝目錄權限；runner 的檔名檢查不能取代二進位身分驗證。工具所在目錄與暫存根目錄必須由受信任的本機程式管理，執行期間不能讓不受信任使用者替換。

請求只有工具 ID、操作名稱和可選的預先開啟 `*os.File`。沒有 shell、HTTP 任意參數、任意環境變數或每次請求的執行檔路徑。註冊時深拷貝 argv 與 operations map；參數不展開變數、萬用字元或模板。Windows 使用明確 application path 與標準 Windows argv quoting，不經 `cmd.exe`／PowerShell。

`Stdin` 是借用的唯讀普通檔案，必須可以 seek。Linux 檢查 `F_GETFL`，Windows 檢查既有 handle 的 `FileAccessInformation`；具有寫入權限的 handle 與 pipe 會被拒絕。呼叫端保留檔案所有權，在 `Run` 返回前不能關閉或並行使用它。子程序可改變共享的檔案位置。根路徑安全開啟與媒體格式策略由輸入 adapter 負責。

## 上限與輸出

| 項目 | 硬性設定範圍／行為 |
| --- | --- |
| 同時執行 | 1–8；名額滿時立即回 `process_busy`，不排無界佇列 |
| 執行期限 | 1 ms–10 min；與呼叫端 context 取較早期限 |
| stdout | 1 byte–16 MiB；成功時僅供內部 parser 使用 |
| stderr | 1 byte–1 MiB；持續讀取與計數，不保存內容 |
| 註冊工具／操作 | 最多 8 個工具、每工具最多 16 個操作；正式註冊目前只允許 ffprobe |
| argv | 最多 64 個參數、單一參數最多 4,096 bytes、總計最多 24 KiB；禁止 NUL／無效 UTF-8 |
| 識別名稱 | 1–64 個 ASCII 小寫字母、數字、`_`、`-` |
| 暫存 | 既有專案專用絕對目錄下，每次建立獨立 `run-*` 工作目錄；回收後刪除 |

stdout／stderr 由兩個固定 goroutine 同時 drain。任一流超限就終止程序群組／Job Object，仍完成 drain 與清理，不把超限內容放入錯誤。所有錯誤只包含固定分類：`process_invalid`、`process_busy`、`process_cancelled`、`process_timeout`、`process_output_limit`、`process_start_failed`、`process_exit_failed`、`process_cleanup_failed`、`process_platform_unsupported`。

出錯時不返回 raw stdout；stderr 永不返回。`Result` 的 JSON 欄位全部排除，預設格式化也只顯示遮蔽文字。成功的 `Result.Stdout` 仍是不可信的內部資料，可能含工具輸出的路徑，不應直接傳給 API 或日誌。

子程序環境只含固定 locale、這次工作目錄的 `TMPDIR`／`TMP`／`TEMP`，Windows 另保留 `SystemRoot`。不繼承資料庫憑據、`PATH`、`LD_PRELOAD`、`FFREPORT` 等環境設定。

## Linux 與 Windows 回收

Linux 為每次執行建立新 process group。取消、逾時、輸出超限，以及直接父程序正常退出時，都對群組送出 `SIGKILL`。先用 `waitid(WNOWAIT)` 觀察退出，終止群組後才 `Wait` 回收父程序，避免在群組終止前放掉父程序 PID。這能處理仍在同一群組中的子、孫程序。

Linux 明確映射的檔案只有 fd 0、1、2，但其他既有 descriptor 必須帶 `CLOEXEC`。Go 標準檔案／pipe API 預設如此；若其他程式碼以 raw syscall 建立非 `CLOEXEC` descriptor，runner 不能保證它不被繼承。後續沙箱 launcher 仍需在 child 內建立嚴格的 descriptor 白名單。

Windows 使用 `STARTUPINFOEX` 的 `PROC_THREAD_ATTRIBUTE_JOB_LIST`，在程序第一條指令前原子加入 Job Object；不使用「先啟動再 Assign」的競態流程。Job 設定 `KILL_ON_JOB_CLOSE`，不允許 breakaway，handle list 僅含 stdin／stdout／stderr。終止時呼叫 `TerminateJobObject` 並觀察 `ActiveProcesses=0`，再關閉 process／job handles。此路徑需要 Windows 10／Windows Server 2016 或更新版本；缺少所需 API 時拒絕執行，不降級為僅殺父程序。建立程序時也設定隱藏視窗。

Windows Job 終止觀察最多 5 秒；父程序結束後的 pipe drain 額外最多 1 秒，逾時關閉讀端並回報清理錯誤。OS 若卡在不可中斷的檔案系統或程序等待呼叫，不能保證所有情況都在設定的毫秒數內返回。若 OS 拒絕 kill 而程序繼續運行，runner 仍保留執行名額並等待父程序結束，不會假稱已終止或釋放名額讓並行量失控；這個錯誤路徑也沒有硬退出期限。runner 會等待自己的 wait／drain goroutine 回收。

## 尚未提供的沙箱能力

程序群組、Job Object、protocol whitelist 與唯讀 fd 都不等於檔案系統／網路沙箱。這個 runner 未限制程序讀取其他主機檔案、建立網路連線、使用記憶體／CPU、寫入磁碟或呼叫 `setsid` 脫離 Linux 群組。固定且已驗證的工具不等於它解析的媒體可信。媒體 probe 必須等待獨立的 OS 沙箱能力驗證，不能以目前的 helper 測試代替。

## 開發用 fixture 生成

只有明確加入 `-tags jelee_fixture_tools` 的建置才包含 `NewFixtureRunner(config, tool)`。它只接受 ffmpeg 身分／檔名及 `version`、`generate-video`、`generate-audio`、`generate-image` 操作名稱。受信任的生成器註冊固定 argv 與專案測試輸出位置，沿用相同的取消、輸出與程序樹管理。

一般 `go build` 沒有此 API；即使加了 tag，普通 `New` 仍拒絕 FFmpeg。此 tag 不由環境變數啟用，也不代表正式服務提供轉碼、remux 或縮圖功能。

## 驗證

測試使用目前 Go 測試執行檔作 helper，不依賴 shell 或真實媒體。覆盖固定 argv 的特殊字元、註冊資料變更隔離、環境清除、seekable 唯讀 stdin、stdout／stderr／雙流洪水、安靜逾時、非阻塞 busy、取消、安全錯誤、直接父程序退出後的子孫清理、啟動失敗，以及重複執行後的 FD／handle、goroutine、暫存目錄與名額回收。

Windows 原生測試與 vet 已通過，套件覆蓋率 86.0%；Linux 同組測試加 race detector 通過，覆蓋率 88.4%。兩個平台的 fixture tag 註冊測試也通過，無跳過。資源數量檢查先暖機 Go runtime 並固定測試的 GOMAXPROCS，避免把 runtime 新建 OS thread 的事件 handle 算成 runner 洩漏。這些是 helper 的程序生命週期證據，尚未宣稱 selected ffprobe binary 的媒體相容性或沙箱驗證完成。

## 原始參考

- [Microsoft：Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)：子程序繼承 job、breakaway 與群組終止語義。
- [Microsoft：UpdateProcThreadAttribute](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute)：JOB_LIST／HANDLE_LIST 的要求與支援版本。
- [Microsoft：TerminateJobObject](https://learn.microsoft.com/en-us/windows/win32/api/jobapi2/nf-jobapi2-terminatejobobject)：終止 job 的全部成員。
- [Microsoft：NtQueryInformationFile](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/nf-ntifs-ntqueryinformationfile)：既有 handle 的 FileAccessInformation。
- 本地 pinned Go 1.27.1 `syscall/exec_windows.go` 與 `golang.org/x/sys v0.48.0/windows`：argv quoting、handle 複製與 attribute list API；未新增依賴。
