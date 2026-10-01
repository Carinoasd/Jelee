# G11.2 可信代理與客戶端地址

Go服務新增 `trustedProxies` 與 `JELEE_TRUSTED_PROXIES`。只有傳輸對端命中明確配置的CIDR，才會使用 `X-Forwarded-For` 作登入限流及帳號稽核地址；未配置時維持傳輸對端。來源地址不改變Host允許名單、資料庫身份或會話權限。

## 設定

同機代理的範例：

```sh
export JELEE_TRUSTED_PROXIES='127.0.0.1/32,::1/128'
```

或在現有JSON設定加入：

```json
{"trustedProxies":["127.0.0.1/32","::1/128"]}
```

環境變數優先；空字串可清除檔案中的白名單。最多64項CIDR，非法值使啟動失敗，錯誤不回顯原值。IPv4、IPv6與映射地址均正規化。請使用實際代理對端網段；Docker橋接來源可能不同於環回範例。

## 鏈與告警

解析器先核對整條XFF鏈，再由右向左剝除可信代理，採第一個未可信地址；全可信時採最左地址。多個同名標頭依到達順序合併。最多8192位元組、32節點，禁止空節點、zone、主機名及port。無效或超限鏈回退傳輸對端並記固定 `invalid_chain` 告警。

未可信的對端帶Forwarded、X-Forwarded-*或X-Real-IP時，忽略地址並記 `untrusted_peer`。每請求最多一筆，包含component、requestId與reason，不包含原始地址鏈或其他敏感值。Forwarded、X-Real-IP、轉發Host／Proto均不參與地址、權限或公開URL重建。

代理須正確清理／追加XFF；來源防火牆由部署限制。沒有可信CIDR時，同一代理後的使用者仍共用傳輸IP限流額度。只有XFF協定接入，完整公開URL與可信代理的公網部署矩陣仍待後續驗收。[網路隱私](network-privacy.md)。

## 驗證範圍

設定來源與預算、CIDR映射／正規化、鏈邊界、重複標頭、超限與非法值均有回歸。實際HTTP Handler驗證不同客戶端限流分離、未可信與非法鏈回退、稽核地址、角色與Host不被提升、告警不洩漏值；真loopback反向代理請求經實際帳號Handler進入repository，地址不出現在回應或請求日誌。

這些驗收不代替公網／來源防火牆或完整WS／SSRF矩陣。G11與全案仍未完成。[證據](evidence/trusted-proxies.json)。

Windows完整Go回歸27套件通過，435項依環境略過並逐項列於證據；vet通過。Linux受影響三套件race通過。移除boundary地址注入時兩項接線驗收失敗，finally來源逐位元還原後正式回歸再通過。
