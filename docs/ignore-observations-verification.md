# 忽略来源逐项证明验证

基于 `37eab90bdd442b37cbb2aa534ed927933aae7cc5`。来源观察新增 `DirectoryProofs()`，返回经过既有双重读取与复核的目录链副本，包含直接父身份、目录身份及固定规则文件的present/absent和完整字节摘要。没有新增I/O、依赖、迁移、HTTP字段或执行开关。数据库持久化和与真实ReadDir句柄连接仍待后续实现。

## 实测

- Windows来源包与架构检查：40个顶层测试通过，vet通过。一个原有symlink权限测试跳过；没有运行Windows race。
- Linux：复制全部matcher/source源文件与go.mod/go.sum到原生`/tmp`独立临时目录，使用锁定Go和已登记GCC执行完整来源包race及vet。42个顶层测试通过、零skip；执行后所有源SHA不变。
- 新增5项测试覆盖真实目录链、present空规则与absent区别、父身份、完整字节SHA/mtime、warm副本隔离、排除子树、缺失目录、复核失败/关闭失败不发布、格式化和JSON脱敏，以及同大小同mtime的内容变化与规则删除。
- 既有来源替换、安全打开、取消、资源关闭、缓存与预算回归均包含在上述来源包测试中。

来源文件和日志SHA见[证据](evidence/ignore-observations.json)。该回归针对来源组件，没有重跑PostgreSQL和真实媒体全量验收；这些执行路径未改变。记录只覆盖一次候选的可达祖先链，不提供原子文件系统快照、已删除目录证明或整库manifest seal。
