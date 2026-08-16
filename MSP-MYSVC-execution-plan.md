# MSP / MYSVC 執行計畫 — 雙軌並行(2 工程師 × Claude Code)

**依據**: MSP-SPEC-001 v1.0 §11、MYSVC-SPEC-001 v1.0 §11
**估算狀態**: 本文件之時程與工作量為**參考基準,非承諾**——實際估算由負責的兩位工程師讀完兩份 spec 後自行給出(per-phase),於 kickoff 會議對齊並取代本文件數字;結構(軌道、里程碑、互審節點、前置依賴)為計畫主體,數字為佔位
**模式**: 三組角色——**Serving team(工程師 A)**主駕 MSP 線、**App team(工程師 B)**主駕 MYSVC 線,各開自己的 Claude Code loop;**Scientist(DS 側)**為兼職第三線,供給模型與判準;跨 contract 邊界互相 review;J 處理決策與 SPEC-GAP 裁定

---

## 1. 開工前(Week 0,不寫碼)

| 項目 | 負責 | 擋住什麼 |
|---|---|---|
| Q7 容量參數(MSP §1.5) | J | MSP Phase 2 起的所有 sizing |
| OQ-1 同 device 順序性(問製程端) | B + J | MYSVC Phase A 派發模型 |
| **前後處理盤點**:既有 MYSVC 內版本相依邏輯清單 + 搬遷量估算(X) | B + scientist 代表 | 各模型 onboard 時程;X 為全案最大未知 |
| OQ-2/OQ-3(subject 結構、下游 upsert 現況) | B | MYSVC Phase B/C 細節 |
| 決定:MYSVC 全面重構 vs 增量改(本計畫假設**全面重構**) | J | MYSVC 全線 |

## 2. 雙軌時程(日曆週,含 auto-loop + review 紀律)

計畫採 **MVP-first**:第一個價值里程碑是「真流量 shadow 上線」(M1),canary/guardrail 為第二階段(M2)。演練依「擋在其保護的能力之前」原則安插,不提前、不省略。

```
Week:        1    2    3    4    5    6    7    8    9   10   11   12   13
A Serving: [P0 contract+工具][P1 sync   ][P2 router][P3 shadow ][P4 eval   ][P5 api+guardrail]
B App:          [PA core ][PB jetstream+DLQ][PC outbound][PD release+obs][搬遷/支援][共同硬化]
C Scientist:[盤點+簽contract][example model+golden]....[Q2].........[M1 首批 onboard][門檻值]
里程碑:      ▲S1        ▲S2                    ▲S3=M1整合            ▲S4        ▲S5=M2
```

Scientist 線為兼職,但三個交付都在關鍵路徑上:**example model + golden samples 擋 Phase 0 驗收(W2 前)**、Q2 擋 Phase 4(W3 前)、M1 後擔任首位使用者走完 onboard(即 M1 的 Done when 驗證)。

- **S1(~W1.5)Contract 凍結會簽**:A 產出 proto/manifest/stub/產生器;B 以 MYSVC 視角 review 後雙方會簽(§1.4 規則)。此後 proto 變更 = breaking,需再會簽
- **S2(~W3.5)Stub 整合**:B 的 Phase C 對 Router stub 打通 predict 往返(不等真 Router)
- **M1 = MVP shadow 上線(目標 W6–8)**:
  - MSP Phase 0–3 完成(計畫值 4–6 週;合成流量產生器驗證,無真流量)
  - **整合演練窗口(~1 週,兩人一起)**:primary 路徑 E2E(UC-0)、deadline 與錯誤語義、trace 串接、**shadow 零影響 chaos 演練**(真 MYSVC 流量下 primary p99 不動)
  - 通過後真流量 shadow 開跑,scientist 即可 onboard 模型累積 evaluation 資料;晉升暫以手動 PR + 人工判讀過渡
- **S4(~W9–10)Evaluation 上線**:Phase 4 完成,dashboard/agreement/accuracy 就位——與 shadow 資料累積期重疊,時程是疊的不是串的
- **M2 = 全能力上線(目標 W11–13)**:Phase 5(msp-api、guardrail、breaker)完成 → **rollback 演練**(guardrail 觸發 → page → 手機核准 → 生效,全程走通;此演練擋第一次 canary,不得省)→ UC-1→UC-6 端到端硬化 → 開放 canary 晉升

**日曆總長:M1 約 6–8 週、M2 約 11–13 週**(不含前後處理搬遷 X;搬遷按模型排程,擋各該模型 onboard,不擋平台里程碑)。「四週完成 MSP」為樂觀下緣非計畫值——成立條件(loop 全程貼上限、review 零積壓、Q7 準時、無大 SPEC-GAP)不宜作為承諾基礎。

## 3. 分工邊界與互相 review(三組角色)

| | Serving team(A,MSP) | App team(B,MYSVC) | Scientist(DS 側) |
|---|---|---|---|
| 主駕 | MSP Phase 0–5 | MYSVC Phase A–D | Example model、golden samples、前後處理搬遷(B 支援) |
| Review / 簽核 | MYSVC 的 ModelClient / 錯誤語義 / trace | msp-contract 全部、Router 錯誤行為、stub | Contract 的 payload schema 與 comparisonPolicy(S1 會簽) |
| 提供 | — | — | Q2 ground truth 定義、per-model 門檻值(M2 前) |
| 共同 | S3 / S5 整合窗口、演練 | 同左 | M1 後首位使用者實走 onboard(驗 Done when) |

Bus factor 緩解:S1 與 S3 兩個節點強制對方深度 review,確保兩人都讀得懂對方線的接點層;純內部實作不互審(靠各自 loop 的驗收測試)。

## 4. J 的節點

- W0:上表五項決策/資料
- S1:contract 會簽(15 分鐘過矩陣即可)
- W3 前:Q2 ground truth 來源(擋 A 的 Phase 4)
- M1 整合演練週:親自盯場(第一個真風險暴露點,兩線未驗證假設在此對撞)
- M2 rollback 演練:驗收(含手機核准實測)
- 隨時:SPEC-GAP 裁定(建議固定每日一次 15 分鐘批次處理,避免兩條 loop 卡著等)
- Kickoff 時向 DS 團隊確認 scientist 線三個交付的時點承諾(example model W2、Q2 W3、M1 首批 onboard)
- 對外溝通口徑:承諾 M1/M2 範圍與目標窗,不承諾單一日期;工程師 per-phase 估算出來後修訂本表

## 5. 能力 Roadmap

### 5.0 Capability Roadmap (one-page)

標記:**[S]** = serving team;**[DS]** = scientist / model center;**[共]** = 雙方。

| | **M1** (W6–8) | **M2** (W11–13) | **v-next: Labeling → Auto-tune** |
|---|---|---|---|
| **Use case** | Self-service publish a new model version to shadow env, validated on live data with zero risk | Self-service promote to production, gradually with instant rollback | Label production cases → auto-retrain → auto-validate in shadow → approve to release |
| **Outcome** | New models fully validated without touching production | Model release becomes routine: gated, gradual, reversible | Models improve automatically as production data and labels accumulate |
| **Done when** | Submit → shadow in ≤15 min → report auto-generated, zero platform involvement | Full self-service promotion, instant rollback, auto circuit-break | Labels feed evaluation and finetune; auto shadow report; humans only approve |
| **Model versioning** | [S] Contract + conformance gate, immutable versions · [DS] example model + golden samples | [S] Release governance, security lane | [S] Auto onboard (webhook) · [DS] auto-publish pipeline |
| Traffic & release | [S] Primary routing (gRPC/HTTP) + full shadow | [S] Canary, sticky, breaker, rollback | [S] Finetune fast lane · [共] shadow 最短時長 |
| Evaluation | [S] Paired diff, accuracy vs ground truth, auto dashboards · [DS] ground truth 定義 (Q2) | [S] Guardrail gating · [DS] per-model 門檻值 | [S] Label join + enforced holdout · [DS] 標注準則、finetune 觸發/停止 |
| Data assets | [S] Full lineage (payload + digest) | — | [S] Sample API, label ingest, dataset export, loop tracing · [DS] 盲標 · [共] labeling UI 歸屬 |
| Self-service | [S] Self onboard; manual promote | [S] Full API/CLI + approvals | [S] Labeling APIs + one-click promote |

讀法:上三列給管理層(一欄一個故事);下五列給工程。每欄站在前一欄之上;Done when 即各階段驗收演練劇本。v-next 內部執行順序仍是先 labeling 後 auto-tune。

### 5.1 v2/v3 分工(serving team vs scientist/DS 側)

原則:**serving team 出機制與資料面,scientist 出策略與品質判準**。

| | Serving team(平台) | Scientist / DS 側(含 model center) |
|---|---|---|
| **v2 labeling** | Sample 供給 API(L1 讀取、presigned payload、disagreement 佇列匯出);label 回寫 contract(`msp.labels` schema、多來源 GT join、provenance);dataset 匯出 API + snapshot id;L1 payload 保留政策 | 標注準則與品質規範(誰能標、標什麼、驗收);active learning 策略(平台給佇列,DS 決定標哪些);label 用於訓練的資料策略 |
| **v3 auto-tune** | Webhook onboard endpoint + 版本自動命名;finetune lane 機制(縮短 shadow 的政策開關);dataset 匯出時**強制 holdout split**;conformance pool 擴容;迴路追查工具(lineage 查詢) | Auto-finetune pipeline 本身(觸發條件、訓練、停止準則);批次發布與自動打包(遵循 MSP-CONTRACT);每版 finetune 的 golden samples 更新;per-model 品質門檻定值(平台執行) |
| **待決策(雙方)** | Labeling UI 歸屬(DS 或 app 團隊,v2 開工前定);finetune lane 的 shadow 最短時長(雙方會簽);盲標模式(UI 側實作,平台提供「不含 prediction 的 sample API」變體) | |

## 6. 風險與對策

1. **X(前後處理搬遷)未知** → W0 盤點強制完成;搬遷與平台解耦(按模型排程),平台上線不等它
2. **Review 跟不上 loop 產出** → 產出強制 PR-sized;loop 夜跑、人日審;寧可 loop 停等 review,不可未審合併
3. **單人單線的知識孤島** → §3 的強制互審節點;S5 兩人共同操作全流程
4. **Q7/Q2 遲交** → 已設為 phase 硬前置(spec 標明),遲交直接反映在時程,不隱性吸收
