# CONTEXT — 領域詞彙表

本檔為 MSP / MYSVC 兩份 spec 的共同語言。只收詞彙定義，不收實作細節；實作歸 spec。

## 角色

- **Scientist**：模型的作者與判讀者。擁有模型內容與 promote 決策權（經核准流程），不碰部署與路由。
- **Model Center**：模型的訓練與打包方。負責「模型是什麼」；版本命名在同一 model 下唯一。
- **MSP（平台）**：模型的路由、驗證、晉升方。不解讀 payload 內容。
- **MYSVC**：模型的呼叫端。只認識邏輯 model name + envelope，對版本與 blue/green 位置無感。

## 模型與版本

- **Model**：一個邏輯推論能力的名字（如 defect-cls）。路由、quota、dashboard 都以 model 為單位。
- **Version**：model 的一個不可變快照。任何行為改變（含只改 threshold）＝新 version。version 字串由 model center 指定，平台視為 opaque，不可重用。
- **Digest**：version 的真正身份（image digest）。平台內部一律以 digest 引用；「驗證過的」與「上線的」必須是同一個 digest。
- **Primary**：正在服務真實流量、其輸出被下游採用的 version。每 model 同時最多一個。
- **Shadow**：接收鏡射流量做推論、但輸出不回傳任何人的 version。零風險，可多個並存。
- **Canary**：承接部分真實流量、輸出被下游採用的 version。每 model 同時最多一個。
- **Promotion**：version 沿 shadow → canary → primary 的晉升。不可跳階。
- **Rollback**：canary 流量歸零或 primary 退回前版。是一種 release，不是刪除。
- **Retired**：不再部署但記錄永久保留的 version；任何歷史版本可重新部署。

## 發布域

- **Blue**：受完整保護的 production 區。只住 primary。
- **Green**：實驗區。住所有 shadow 與 canary。green 不得排擠 blue。
- 注意：blue/green 是**模型的發布域**，不是整個系統的發布域——MYSVC 單一部署，不隨 blue/green 複製。

## 契約

- **MSP-CONTRACT**：平台對 model image 的全部要求（envelope、manifest、行為要求）。變更需平台與 application 團隊雙方會簽。
- **Envelope**：所有模型統一的 gRPC 請求/回應外殼。平台只驗 envelope，payload 內容屬模型。
- **Payload schema**：payload 的實際結構，由各模型宣告。是 MYSVC 與模型之間唯一的耦合點。
- **Conformance**：模型進入平台的唯一入口——自動化驗證 image 是否符合 MSP-CONTRACT。通過＝可部署，不靠文件與自覺。
- **Golden sample**：模型自帶的輸入/預期輸出樣本，conformance 與探測（probe）用。

## 評估

- **Paired diff**：同一筆請求下，shadow 輸出 vs primary 輸出的逐筆比對。
- **Agreement**：paired diff 的一致率。**是變化偵測器，不是品質分數**——新模型修正舊錯誤同樣拉低 agreement。低 agreement 意為「變化大、需要人看」。不得單獨用於放行或否決。
- **Ground truth**：事後到達的真實標籤（來源 Q2，scientist 定義）。品質的最終裁判是 accuracy vs ground truth，不是 agreement。
- **Shadow miss**：時限內 shadow 結果未到齊的請求。不進 agreement 分母，單獨計比率；比率超標則整段 shadow 數據不可信（promotion 硬條件之一）。
- **Unlabeled**：超過時限仍 join 不到 ground truth 的預測。unlabeled 比例本身是指標。
- **Guardrail**：canary 期間的自動監控與止損機制。觸發後準備 rollback，由人核准生效。

## 詞彙紀律

- 「上線」一律指 **promotion 至 primary**；shadow 開跑不叫上線。
- 「回退」與「rollback」同義，指路由層操作（秒級）；不指重新部署。
- 「device」指 fab 機台（device_id 的主體），不指終端使用者裝置。
