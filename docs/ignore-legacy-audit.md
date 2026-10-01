# 舊格式相容的版本核對

本輪延續[原始來源審計](ignore-source-audit.md)，補上依賴的精確版本與實際執行證據。尚未啟用舊格式解析。

## Ignore 0.2.1 的來源與實測

從[NuGet固定版本](https://www.nuget.org/packages/Ignore/0.2.1)下載套件，nupkg SHA256為 `54796c77755af4837911b0071621070ef2575a16f2517c599c6acc9a1ff590f5`。套件的 nuspec 指向來源提交 [`f7c6f07d66d0e1043d901a2ab2f58daca1862066`](https://github.com/goelhardik/ignore/tree/f7c6f07d66d0e1043d901a2ab2f58daca1862066)。授權為MIT、Copyright 2020 Hardik Goel；未把來源複製進正式程式。

已比對四個來源blob，並以未修改的C#來源編譯執行205組輸入（13組固定案例及24種模式×8種路徑）。使用本機.NET 10.0.11、zh-TW文化設定；未定義NET8_0_OR_GREATER，因此走runtime Regex分支，不能宣稱已驗證.NET8 source-generator分支或完整上游測試套件。重現腳本為 `scripts/test_ignore_upstream.ps1`，預設讀取 `.testdata/ignore-upstream`，執行前強制檢查來源blob雜湊。[原始結果](evidence/ignore-upstream-semantics.json)。

已確認的差異：

- library層的空集合與只有註解不排除；包裝器對空白全文的全排除是另一層行為。
- ASCII大小寫不敏感。
- `/a.mkv` 對 `a.mkv` 成立，對 `/media/a.mkv` 不成立；包含中間slash的規則也受輸入完整路徑影響。
- `(foo|bar).mkv` 可匹配 `foo.mkv`：部分regex符號未被當作字面值處理。
- `[` 會拋RegexParseException；包裝器如何處理例外必须另外核對。
- 直接查詢子路徑時，否定可取消目錄規則的排除；不能據此推論遍歷已剪枝目錄後仍會進入子項。

因此不能直接把自有Git式matcher加上檔名別名，就宣稱相容此依賴。

## Go轉換器進度

新增 `internal/platform/legacyignore.Translate`，固定轉換順序與版本識別，僅產生上游regex文字、否定與空白/註解標記，不執行來源探索或正式掃描。輸入限制4096 bytes且須合法UTF-8、無NUL；保留衍生來源MIT授權於同目錄 `LICENSE.ignore`。

205組差分比較C#反射取得的實際regex文字與否定標記；所列案例亦比較Go regexp的匹配結果。Windows test/vet、Linux race、架構測試均通過。這些案例不涵蓋所有.NET regex或文化相關Unicode折疊，尚不能把此轉換器當作完整相容matcher；unsupported語法不能靜默當成上游無效規則或未匹配。包裝器最近来源/Trim/例外政策、來源家族持久合同及scanner接線仍待完成。

## 其他上游版本與格式

精確的專案名稱、固定提交、檔案連結與官方文件集中於[来源索引補充](00-audit-baseline.md#8-忽略格式來源補充2026-10-01)。v10.11.0的包裝器與本基底不同：目錄只看最近.ignore全文是否空白；非空規則只對檔案求值，沒有新版TrimEntries與逐行regex例外略過。

另一上游的4.8目錄標記與4.9文字規則也不能混為同一格式。其公開主線仍是2018年的3.5.3，不能当成4.9來源。G22.2另外兩個專用檔名目前均缺足以實作精確語義的對應來源，不能自行假定別名或把目前code search零筆視為所有歷史不存在。
## 下一步

先為本專案已固定的.ignore包裝器建立獨立相容合同與差分案例，包含最近來源、空白、Trim、regex例外、完整路徑及大小寫。來源讀取仍遵守庫根邊界與no-follow，不複製宿主祖先搜尋或連結穿越。來源家族必須進入持久意圖、證據與報告；不能讓舊worker誤認新語義，也不能修改已發布遷移。

G22.2另外兩個專用檔名的對應版本來源不足仍是獨立未完成項，不能使整個G22提前變成已完成。

## 相容引擎邊界評估（尚未選定正式依賴）

Oracle共253組：原205組加48組引擎邊界，包含反向引用、Unicode類別、希臘字母與拉丁字母大小寫、補充平面字元、跳脫/字元集合/量詞及.NET不接受的PCRE語法。48組另存engineCases；Go測試對其中上游成功編譯的規則核對轉換文字，不拿RE2編譯失敗冒充上游無效。

獨立工具模組`scripts/ignore-engine-eval`固定regexp2 v1.12.0與v2.8.1及go.sum；不加入正式server依賴。從倉庫根執行`./scripts/run-go.ps1 -C scripts/ignore-engine-eval run .`重現；輸出差異列表，零退出碼代表評估程序完成，不代表各候選相容。

| 引擎 | 原始rune模式/輸入差異 | UTF-16模式/輸入差異 |
|---|---:|---:|
| v1.12.0 | 8 | 0 |
| v2.8.1 | 20 | 13 |

v2的syntax/charclass.go使用unicode.SimpleFold，加入了上游此runtime/culture不接受的等價字元；另外接受上游拒絕的\Q、\R、\X。不能直接採用。

新增UTF16Pattern將非BMP模式字元轉為surrogate unicode escapes，保留量詞作用於單個UTF-16單元的語義。若字元前有奇數個backslash，生成的unicode escape取代最後一個backslash；新增的`\😀`案例證明直接逐字轉換會改變結果。評估器以MatchRunes接收UTF-16單元。[評估摘要](evidence/ignore-engine-evaluation.json)保存oracle雜湊與各模式結果。

253組吻合只支持繼續評估v1，尚不證明完整.NET相容。來源檢查確認v1的runner.go/doubleIntSlice沒有回溯stack硬上限；MatchTimeout不接受context，compile也無取消入口；逾時錯誤包含完整輸入，不可直接公開。背景時鐘預設100ms週期，額外延長約1秒才自動結束。不能用一個可提前返回的goroutine包裝，就宣稱工作已取消。正式執行層需真正的取消、編譯/匹配資源限制、診斷遮蔽與文化版本合同後才可接入。

本小階段的Windows test/vet與Linux race（legacyignore 1.118秒、architecture 1.132秒）通過。主go.mod/go.sum、migration及掃描行為未改；來源探索、持久family/API/scanner仍待完成。
