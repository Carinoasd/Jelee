# NFO 原文綁定與進程內去重

`Writer.Replace`接收`ReadSource`的原文觀察、受控修改Document及備份份數。Source私下保留root、父目錄與檔案身分、路徑與讀取上限，仍無開啟的handle；JSON及String/GoString診斷不暴露原文或路徑。讀取也新增父目錄前後身分核對。writer重新開啟root與父目錄並持有到操作完成，拒絕符號連結元件；取得native鎖後、備份輪替前及替換前重新核root/父目錄/原檔身分與完整bytes。檔案在相同bytes/size/mtime下換成新inode，或父目錄/root換成新實體並保留hardlink，均拒絕。

修改Document必須由同一原文的WithText/WithTextOptions或EnsureID/EnsureIDValue產生，可連續修改；私有原文雜湊證明綁定最初bytes。任意Read得到的替換XML或從其他原文修改的Document不能寫入。Metadata/Entries公開視圖的變動不能改原文、解除鎖或偽造修改證明。既有ID保持；缺ID於共用操作內生成，詳見[ID策略](nfo-generated-id.md)。已有ID的零變更原文可保持原inode。替換仍受原始ReadSource的maxBytes限制。

單一Writer實例使用singleflight，只共用同時存在的相同完整意圖：原文stamp、新內容雜湊、原始讀取上限、備份份數及路徑一致，且root/父目錄/檔案身分均相同。不同Source讀取若指向同實體也可共用；同一路徑換成不同實體會分開。意圖key為固定雜湊/數字，不含原文或路徑；引用在每個呼叫返回時清除，不保存完成結果。不同修改或備份選項不合併，原文已改變時後來的操作拒絕，須重新觀察。

第一個呼叫的context擁有共用工作。等待者取消只停止自己的等待；擁有者取消時若工作已開始，會等待工作清理/提交結果再返回，其他共用者收到同一結果。原子替換後仍完成提交/回滾；不能因取消而留下半份XML。若擁有者在工作開始前取消，工作context已取消，不進行檔案寫入。Writer必須共用且使用後不可複製。

Windows nfo/architecture、jobs/runtime與nfo vet通過，[Linux nfo/architecture race](evidence/nfo-writer-race-linux.txt)通過。以每次呼叫的私有提交觀察與阻塞sync控制驗證：同意圖/獨立同源觀察只替換一次，不同文字/備份選項不共用、等待者取消不取消擁有者、擁有者取消清理後所有共用者返回取消、完成意圖不保留。另核任意XML/不同修改來源拒絕、多次文字修改/缺欄位新增/手工ID保持、相同bytes與mtime的實體替換拒絕與診斷隱私；前批100次同程序/跨程序替換仍回歸通過。

## 共用 CPU 與 I/O 配額

`NewWriterWithBudget`接收呼叫者共用的`app.WorkBudget`，拒絕nil。每次singleflight共用操作先取得CPU，完成原文解析、缺ID生成及兩份XML嚴格驗證後釋放；再取得I/O，開啟root/父目錄、等待native檔案鎖、複核來源、暫存/備份/替換/回滾，直到全部handle與暫存清理後釋放。兩階段不巢狀，total=1可完成。呼叫者不得持有同一budget配額進入Replace。

配額滿載原樣回報`domain.ErrResourceBusy`，排隊可取消；取得I/O前不建立鎖旁檔、備份或暫存。等待者取消不釋放擁有者持有的I/O；共用操作完成才回收。零值Writer仍供獨立adapter呼叫者使用，沒有配額。正式runtime尚未建立此writer或接入持久寫回jobs；未宣稱所有NFO寫回已由runtime配置控制。

Windows nfo/architecture、jobs/runtime及vet通過；[Linux nfo/architecture race](evidence/nfo-writer-budget-race-linux.txt)通過。真budget驗證CPU→I/O順序、total1實際替換、sync失敗與兩種滿載拒絕、CPU/I/O排隊取消後無檔案副作用與可重試、共用等待者取消時擁有者配額保持；完成後active/waiting歸零。既有native鎖、原文身分、singleflight及回滾矩陣也回歸。

G39.6/8/9/10/14/15仍未完成。這是adapter writer，尚未接正式read-write庫策略、持久jobs、項目與租約/generation授權、媒體身分複核及提交恢復稽核。Windows落盤與完整ACL/owner保留、缺失NFO建立、完整欄位、真實上游客戶端與正式混合負載仍待驗收；實體複核與rename之間的外部修改競態仍非檔案系統快照。
