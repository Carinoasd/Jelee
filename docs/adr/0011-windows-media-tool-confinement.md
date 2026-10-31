# ADR 0011：Windows 媒體探測以受限 token＋Job Object 隔離，不採 AppContainer

- 狀態：已採納（Windows 實機驗證待擁有者執行，見[驗證清單](../owner-verification-queue.md) B18、C36、D36）
- 日期：2026-10-30
- 相關需求：G09.6（並涉及 G09.2、G09.4、G09.5）
- 相關程式：`internal/platform/sandbox/windows_policy.go`、`direct_windows_amd64.go`、`internal/platform/process/child_confined_windows.go`、`isolated.go`、`internal/platform/proberuntime/`、`internal/platform/runtime/probe.go`
- 詳細說明：[媒體 helper 隔離：Windows](../media-sandbox.md#windows-amd64-confinement-g096)

## 需求原文

> G09.6 权限：以低权限运行外部工具（容器非 root 优先）。
>
> G09.4 执行控制：超时、取消（进程组终止）、并发上限、输出大小上限、临时目录隔离与清理。

## 背景

Linux 的探測隔離是「本程式以 helper 模式啟動 → 子程序內自行套用 Landlock、seccomp、rlimit → `execveat` 換成已驗證的 ffprobe」（[ADR 0001](0001-external-process-start.md)、[媒體 helper 隔離](../media-sandbox.md)）。Windows 沒有「在原程序內換映像」，也沒有 Landlock／seccomp，過去 `sandbox.New` 在 Windows 一律回 `media_sandbox_unsupported`，探測整個停用。

## 決定

Windows amd64 改為「由父程序驗證並釘住工具映像，再以受限身分直接啟動工具」，沒有 helper：

1. **映像身分與釘住。** `sandbox.New` 在 Windows 只接受乾淨的磁碟機絕對路徑（拒絕 UNC、`\\?\`、相對、`..`、ADS、結尾點／空白），檔名必須是 `ffprobe.exe`；policy 只有清單內的 SHA256（Gyan 靜態版，不接受函式庫清單）。檔案以 `FILE_SHARE_READ`（不含寫入、刪除共用）開啟，拒絕 reparse point、目錄、裝置，`GetFinalPathNameByHandle` 必須等於要求的路徑（擋祖先目錄的 junction／符號連結），雜湊、PE（AMD64、可執行、非 DLL）檢查都在這個 handle 上做；launcher 存活期間一直持有它，所以工具不能被改寫、改名或刪除，`CreateProcess` 映射到的就是雜湊過的位元組。
2. **受限 token。** 由服務自己的 token 以 `CreateRestrictedToken(DISABLE_MAX_PRIVILEGE)` 產生：除 `SeChangeNotifyPrivilege` 外的特權全部移除；Administrators、Power Users、Backup Operators 等 9 個特權群組（固定清單，見 `windows_policy.go`）改為 deny-only；強制完整性標籤設為 **Low（S-1-16-4096）**。建立後把完整性、deny-only、特權逐項讀回核對，任何一項不符就不啟動。
3. **Job Object。** 子程序建立前就建好 Job：`KILL_ON_JOB_CLOSE`、`ACTIVE_PROCESS_LIMIT=1`（只有工具本身）、每程序已提交記憶體 2 GiB、使用者模式 CPU 30 秒、`DIE_ON_UNHANDLED_EXCEPTION`、不允許 breakaway，以及全部 8 項 UI 限制（桌面、顯示設定、ExitWindows、全域 atom、他程序 USER handle、剪貼簿讀寫、SystemParametersInfo）。另加 `PROC_THREAD_ATTRIBUTE_CHILD_PROCESS_POLICY = PROCESS_CREATION_CHILD_PROCESS_RESTRICTED`，即使 Job 計數之外也不能建立子程序。
4. **啟動順序。** `CreateProcessAsUser(受限 token, …, CREATE_SUSPENDED)`，同時以 `PROC_THREAD_ATTRIBUTE_JOB_LIST` 原子放進 Job、`HANDLE_LIST` 只繼承 stdin／stdout／stderr；暫停狀態下以 `IsProcessInJob` 確認 Job 成員、開子程序 token 再核對一次完整性／deny-only／特權，全部通過才 `ResumeThread`；任一失敗即終止子程序並關 Job。
5. **參數與 I/O。** argv 與 Linux 完全相同（`metadataArguments`／`coverArguments`，媒體以 `fd:` 從 stdin 讀）；命令列由 `sandbox.WindowsCommandLine` 依 CommandLineToArgvW／MSVC CRT 規則組字串（與 `syscall.EscapeArg` 同規則，程式名一律加引號、拒絕含引號、NUL、無效 UTF-8 與超過 32767 字元）。媒體以父程序已開啟的唯讀 handle 繼承，工具不需要也不會以路徑開啟媒體；stdout／stderr 是有上限的 pipe。逾時、取消、輸出超限都 `TerminateJobObject` 並等到 Job 內沒有存活程序。
6. **上層介面與錯誤碼不變。** `proberuntime.New`／`NewCover` 回同一個 `*process.IsolatedRunner`；`Diagnose`／`jelee-cli doctor probe` 用同一份「故意無效的健康位元組」，只有 ffprobe 的正常拒絕（exit 1）才算 available（Windows 的 reason 是 `confined_process_verified`）。錯誤仍是固定 sentinel（`media_sandbox_invalid`／`_unavailable`／`_unsupported`、`process_*`）。沒有 helper，所以 exit 64／78 是工具自己的退出碼，一律歸為 `process_unexpected_exit`。
7. **工具位置。** 只由嵌入清單決定：`<安裝根>\.tools\media\windows-amd64\gyan-9.0.2\…\bin\ffprobe.exe`，安裝根是執行檔所在目錄（`bin\`）的上一層；不看工作目錄與環境變數。

## 低完整性對媒體目錄的影響

- **讀取不受影響。** Windows 的強制完整性預設只有 No-Write-Up，沒有 No-Read-Up（檔案預設標籤不含 `NO_READ_UP`），Low IL 程序仍可讀取服務使用者能讀的檔案，所以工具能載入自己的映像與系統 DLL。媒體本身經繼承的 handle 讀取，連路徑讀權限都用不到。
- **寫入被擋。** 一般檔案、目錄、登錄機碼、具名物件都是 Medium 或未標示（視為 Medium），Low IL 無法寫入，包括 Runner 為每次執行建立的私有 `run-*` 目錄與 `%TEMP%`。探測工具本來就不寫檔：輸出只走 pipe。日後若有需要寫檔的工具模式（例如抽取），應建立專用、標示為 Low 的私有目錄，而不是放寬 token。
- **未加 restricting SIDs。** 加了會讓 token 對每個存取做第二次檢查，使用者 SID 不在其中就讀不到媒體與工具本身；deny-only＋Low IL 已足以擋寫入與特權。

## 為什麼不用 AppContainer（列為後續選項）

AppContainer（或 LPAC）隔離更強（獨立的物件命名空間、預設拒絕檔案存取），但：

1. **需要部署 ACL。** AppContainer SID 預設讀不到使用者的檔案。工具目錄、系統 DLL 之外的相依、以及每個媒體根目錄都要另外授權（`ALL APPLICATION PACKAGES` 或該容器 SID 的 ACE）；這會改動使用者的媒體目錄 ACL，或要求安裝程式管理授權，與「只讀、不改原媒體」的原則衝突。
2. **需要 profile 與 capability 管理。** 要 `CreateAppContainerProfile`／刪除、決定 capability 清單，服務帳號換人或重灌時還要清理；LPAC 連部分系統 DLL 都要額外授權。
3. **複雜度與可驗證性。** 目前沒有 Windows 實機 CI 可跑沙箱負向測試；先用語義清楚、只靠 token 與 Job 的方案，把可驗證的邊界做穩。

本方案已滿足 G09.6「低權限執行」：無特權、特權群組 deny-only、Low IL 不能寫、單程序、資源受限。AppContainer 留待有實機驗證與安裝程式後再評估，屆時需另立 ADR 並改 `WindowsMetadataPolicyVersion`。

## 範圍與已知限制

- **只涵蓋 ffprobe（metadata 與內嵌封面）。** MediaInfo、mkvtoolnix、Tesseract 的 Linux 模式讓工具以 `/proc/self/fd/0` 重新開啟 stdin；Windows 沒有對應的路徑，MediaInfo CLI 從 stdin 讀的行為也未經驗證，若改成傳媒體路徑會違反「使用者資料不成為參數」（G09.2）。這些模式在 Windows 仍是 `platform_unsupported`，`Confinement` 與 `startConfinedChild` 已是通用的，之後只需各自決定輸入方式。
- **目錄探測已開放（遷移 000108）。** 原先 `tool_versions.platform` 與 `domain.ValidateProbeIdentity` 只收 `linux-amd64`（遷移 000004 的 CHECK），Windows 啟動時即使沙箱通過也以 `catalog_identity_unsupported` 停用目錄探測。遷移 000108（暫定編號）讓 CHECK 與驗證函式接受 `windows-amd64`；Windows 探測身分由 `proberuntime.Identity` 以清單中的 ffprobe.exe 雜湊、固定的靜態版 runtime 標籤（無函式庫清單）、與 Linux 相同的固定 argv 摘要，以及 `WindowsMetadataPolicyVersion` 組成，平台字串本身在身分摘要裡，所以 Linux 與 Windows 的快取結果永不共用。Windows 啟動時跑同一個健康檢查：通過即啟用目錄探測，失敗時記 WARN（`health_check_failed`）並維持停用，沒有未隔離的備援。降級遷移在有任何非 Linux 身分列時拒絕（`55000`），由操作者先清掉探測快取。`catalog_identity_unsupported` 這個原因已移除。只開放 ffprobe；`jelee-cli doctor probe` 與初始化向導的工具偵測照舊會實際跑受限 ffprobe。
- **服務身分。** 以 LocalSystem 執行時，Low IL 仍擋寫入，但 SYSTEM 能讀的範圍很大；建議以專用標準使用者執行服務（見[部署](../deployment.md#windows-上的媒體探測g096)）。
- **釘住的副作用。** launcher 存活期間無法更新或刪除 `ffprobe.exe`，也不能改名它的上層目錄；更新工具要先停服務。

## 後果

- 新增 `WindowsMetadataPolicyVersion = "windows-metadata-sandbox-v1"`；token、完整性、Job 限制或子程序政策任何改動都必須改版本。
- Linux 的 helper、descriptor、`linux-metadata-sandbox-v1` 與探測快取身分完全不變。
- `CreateProcess`／`CreateProcessAsUser` 仍只出現在 `internal/platform/process/child_*.go`（ADR 0001 的守門 grep 照用）；x/sys 沒有封裝的 `CreateRestrictedToken`、`IsProcessInJob` 以 `windows.NewLazySystemDLL`（只從 System32 載入）呼叫，未新增相依。
