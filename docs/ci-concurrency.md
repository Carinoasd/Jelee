# CI 執行並行控制

連續階段推送時，舊提交的CI仍佔用runner，最新提交處於queued。六份CI父工作流程已新增根層級並行控制，按workflow、event與ref分組；新觸發可取消同組尚未完成的執行，減少被取代的工作占用。各工作流程、事件與分支互相隔離，push與PR驗收均保留。

```yaml
concurrency:
  group: ${{ github.workflow }}-${{ github.event_name }}-${{ github.ref }}
  cancel-in-progress: true
```

GitHub支援根層級concurrency與cancel-in-progress，且建議讓不同workflow使用不同群組；這份設定沿用該機制。[官方文件](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)

修改範圍為Go foundation、三平台C#測試、格式、ABI、CodeQL及OpenAPI PR檢查。六份YAML解析通過，移除新增concurrency欄位後與基底所有欄位逐項相同；觸發條件、權限、jobs、steps、矩陣、門禁與制品保留均未改。可重用或發布工作流程未套用此設定。

## 已執行的清理

以同分支遠端head作保護，盤點19個舊head活動run；逐筆再次核對head、branch、event與status後送出一般取消。17個已確認cancelled，兩個在核對時已自行終止（success／failure各一）。未取消當時最新head、其他分支、人工／排程事件或已完成執行。取消後的記錄是cancelled，不計為成功。

04a80ad9f9舊head的兩平台Go及真PostgreSQL已通過，整份workflow的failure來自完整品牌門禁。新head仍必須核對自己的CI；這項排程改善沒有清除14,735項品牌殘留或實際ABI差異，也不代表全案通過。

[來源與執行證據](evidence/ci-concurrency.json)。
