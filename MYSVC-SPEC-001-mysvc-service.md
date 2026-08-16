# MYSVC-SPEC-001 — MYSVC(Model 呼叫端服務)設計文件與規格

**Status**: v1.0 — 凍結供實作(2026-08-13)。遺留項:OQ-1 順序性(製程端,Phase A 前)、OQ-2 subject 結構、OQ-3 下游 upsert 現況
**Scope**: MYSVC(model 呼叫端服務)之架構重構、JetStream 消費、與 MSP 平台對接、發布管理
**關聯**: MSP-SPEC-001(Model Serving Platform);本文件為呼叫端視角,兩份 spec 的接點見 §10
**Audience**: 架構設計文件 + Claude Code 實作規格

---

## 1. 目標與範圍

### 1.1 目標

1. MYSVC 自 NATS **JetStream** 消費 service input,經 MSP Router 呼叫模型(gRPC, MSP-CONTRACT envelope),結果續行下游流程
2. 架構重構為六邊形(ports & adapters):訊息來源、模型呼叫、下游輸出皆可替換,core 純業務邏輯
3. 資料可靠性:at-least-once + 冪等,不丟不卡
4. 自身的發布管理:rolling update 為常態,支援 canary 與(roadmap)shadow mode

### 1.2 非目標

- 模型的 shadow/canary/切換(屬 MSP,MYSVC 無感)
- 隨模型版本演變的前/後處理邏輯(依 MSP-SPEC-001 §2.3 紀律,封裝於 model image,MYSVC 不得持有)

### 1.3 核心設計原則

| 原則 | 說明 |
|---|---|
| **Thin adapter, pure core** | Listener 只做收→轉→交;core 不 import 任何 broker/gRPC/protobuf 型別 |
| **At-least-once + 冪等** | 處理完成才 ack;重複靠冪等 key 吸收 |
| **Fail loud, not stuck** | Poison message 進 DLQ + 告警,絕不卡死 stream |
| **Backpressure by pull** | Pull consumer,worker 滿則不拉,無無限堆積 |
| **與 model 生命週期解耦** | 只認邏輯 model name + envelope;version/位置由 Router 解析 |

---

## 2. 架構分層

```
┌─────────────────────────────────────────────────────────┐
│                    Inbound Adapters                      │
│   JetStreamListener(pull consumer, ack 管理, DLQ)       │
│   [KafkaListener — 若有 Kafka 來源,同介面另一實作]        │
└────────────────────────┬────────────────────────────────┘
                         ▼  內部 domain 物件(InspectionInput)
┌─────────────────────────────────────────────────────────┐
│                      Core Domain                         │
│   分類流程編排:input → 呼叫模型 → 判讀 → 產出結果         │
│   純 Java,無 broker/gRPC/protobuf 依賴,可獨立單元測試    │
└──────────┬──────────────────────────────┬───────────────┘
           ▼ port: ModelClient            ▼ port: ResultPublisher
┌─────────────────────┐        ┌─────────────────────────┐
│  Outbound Adapters  │        │  Outbound Adapters       │
│  GrpcModelClient    │        │  DownstreamPublisher     │
│  (→ MSP Router)     │        │  ShadowPublisher(§8.3)   │
└─────────────────────┘        └─────────────────────────┘
```

- 內部溝通一律使用 domain 物件;broker 的 wire format 與 MSP envelope 只存在於 adapter 內
- Shadow mode = 同一 core + 替換 ResultPublisher 實作(§8.3),架構層面免費獲得

---

## 3. Inbound — JetStream Listener 規格

### 3.1 Consumer 形態

- **Pull consumer, durable**:MYSVC 控制拉取節奏,backpressure 天然成立;重啟自 durable 進度續行
- Batch fetch(建議 batch size = worker 併發度的 1–2 倍),搭配 max in-flight 上限

### 3.2 Ack 參數

| 參數 | 設定 | 說明 |
|---|---|---|
| Ack policy | explicit | 模型呼叫 + 下游寫入皆成功後才 `ack()` |
| AckWait | p99 端到端處理時間 × 2–3 | 過短會在處理中重投,製造假重複 |
| MaxDeliver | 3–5 | 到頂觸發 advisory → DLQ 流程(§3.3) |
| In-progress | 長尾請求呼叫 `inProgress()` 延長 | 應對偶發慢請求,避免誤重投 |

失敗處理:可重試錯誤 `nak(delay)` 退避重投;明確不可重試(schema 錯誤等)直接走 DLQ 流程不耗 MaxDeliver。

### 3.3 DLQ(JetStream 無內建,自建)

1. 監聽 `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES` advisory
2. 依 advisory 中 stream sequence 撈取原訊息 → 寫入 `MYSVC_DLQ` stream(附:原 subject、投遞次數、最後錯誤摘要)→ term 原訊息
3. DLQ 進訊息即告警;提供 replay 工具(修復後重新投遞回原 subject)
4. DLQ stream 保留期 ≥ 30 天

### 3.4 Subject 設計

- Input:`mysvc.input.{device_id}`(或既有 subject 結構,以 device id 可提取為原則)
- 順序性依 §6 決策;subject 粒度支援未來 per-device 的 canary 指派(§8.2)

---

## 4. Core Domain

1. 編排:`InspectionInput → ModelClient.predict() → 判讀 → InspectionResult → ResultPublisher.publish()`
2. 禁止依賴:`io.nats.*`、`io.grpc.*`、generated protobuf 型別、Kafka client——違者 build 檔案的 ArchUnit 測試直接擋下
3. 判讀邏輯限「與模型版本無關」的業務規則;版本相依邏輯屬 model image(MSP-SPEC-001 §2.3)
4. Core 單元測試以 mock ports 進行,不起任何 broker/服務

## 5. Outbound Ports

### 5.1 ModelClient(→ MSP Router)

- 實作:grpc-java / Spring gRPC,MSP-CONTRACT envelope(MSP-SPEC-001 §4.1)
- **ManagedChannel 單例復用**;`withDeadlineAfter()` 每呼叫設定,deadline 沿鏈傳遞
- Canary fail-fast(MSP Q3,含 circuit breaker 修訂 Q3-R)之錯誤回傳:對 MYSVC 即一次可重試失敗,依 §3.2 nak 退避——**不在 MYSVC 內實作「改打其他版本」的邏輯**(路由是 Router 職責);`INVALID_INPUT` 為不可重試,直接走 DLQ 流程(對應 MSP-SPEC-001 §4.3-5 runtime schema 驗證)
- Metrics:per model 呼叫延遲、錯誤率(供與 MSP 端指標對賬)

### 5.2 ResultPublisher

- `DownstreamPublisher`:寫真實下游(topic / DB,依現行)
- 寫入必須 **upsert / 冪等**(§7)
- `ShadowPublisher`:寫 `mysvc.shadow.results` stream(§8.3),介面同一

## 6. 併發與順序模型

- Consumer/fetch 執行緒只收不算;處理派發至 worker(**Java 21 virtual threads**,IO-bound 適用),max in-flight 上限即 backpressure
- **同 device 順序性 = 開放問題 OQ-1(§12)**,需製程端確認:
  - 若需嚴格順序:per-key(device_id)串行派發(key-based executor),跨 device 併發
  - 若可容忍亂序:全併發,吞吐最佳
- 注意:JetStream 重投本身會造成亂序;若 OQ-1 答案為「需嚴格順序」,重投策略需一併設計(同 key 阻塞等待重投完成)

## 7. 冪等與去重

- 冪等 key:訊息內業務 id(建議)或 JetStream stream sequence
- 下游寫入一律 upsert by 冪等 key;無法 upsert 的下游前置去重表(key + TTL ≥ AckWait × MaxDeliver 時間窗)
- 重複到達視為正常事件(at-least-once 的代價),計 metrics 不告警

## 8. 發布管理

### 8.1 常態:Rolling Update

無狀態 + durable consumer 前提下,rolling 即可:舊 instance 停止 fetch → 處理完 in-flight → 下線;JetStream 自動把訊息給存活 instance。

### 8.2 Canary(需要時)

- 新舊版本同一 durable consumer group 概念下按 instance 數量比例分擔(9 舊 + 1 新 ≈ 10%)
- 精準指定 device:利用 subject 粒度(§3.4),canary instance 訂閱指定 device 的 filtered consumer
- 治理:MYSVC canary 與 model canary **不在重疊 device 上同時進行**(變更日曆錯開),確保指標可歸因

### 8.3 Shadow Mode(roadmap)

- Shadow 部署 = 同版本或新版本 MYSVC + `ShadowPublisher`(§5.2)+ 獨立 durable consumer(消費同樣 input,進度獨立)
- 輸出進 `mysvc.shadow.results`,比對 job 對照真實下游結果評估新版行為
- 前提:比對基準與判定準則另定;v1 不實作,架構已預留(publisher 可換即成立)

### 8.4 大改版:Blue/Green

換框架/序列化等重大變更時:green 全量部署(不啟動消費)→ blue 停止 fetch 並排空 → green 以同 durable 接手。中斷秒級,offset 不丟。

## 9. Observability(接 LGTM)

- Metrics:fetch/處理延遲(histogram)、in-flight 數、ack/nak/term 計數、重投率、DLQ 進入數、ModelClient per-model 延遲與錯誤率、下游寫入延遲
- Traces:OTel — JetStream 收訊 → 模型呼叫(串接 MSP request_id)→ 下游寫入,全鏈一條 trace
- 告警:DLQ 進訊息、重投率異常、consumer lag(pending count)超閾值

## 10. 與 MSP-SPEC-001 的接點

四方責任矩陣(Scientist / Model Center / MSP / MYSVC)與各邊界介面定義於 MSP-SPEC-001 §1.4,本文件遵循之;MSP-CONTRACT 變更需平台與 application 團隊雙方會簽。

| 接點 | 本文件 | MSP-SPEC-001 |
|---|---|---|
| Predict 呼叫 | §5.1 ModelClient | §4.1 envelope、§6 Router |
| Payload schema 演進 | MYSVC 端 expand-contract 升級 | §2.3 規則、C4 判定 |
| Canary fail-fast | §5.1 重試處理 | Q3 決議 |
| 變更日曆 | §8.2 治理 | canary 期間協調 |
| Trace 串接 | §9(request_id) | §4.1 request_id |

## 11. 實作計畫

### Phase A — 骨架與 Core
- 產出:六邊形骨架、domain 物件、core 編排 + mock ports 單元測試、ArchUnit 依賴規則
- 驗收:core 測試不起任何外部依賴全綠;違規 import 被 build 擋下

### Phase B — JetStream Inbound + DLQ
- 產出:pull consumer(§3 全參數)、DLQ advisory 監聽與 MYSVC_DLQ 寫入、replay 工具
- 驗收:kill -9 不丟訊息(重投吸收);poison message 3 次後進 DLQ 且 stream 不卡;backpressure 生效(worker 滿即停拉)

### Phase C — Outbound + 冪等
- 產出:GrpcModelClient(channel 復用、deadline)、DownstreamPublisher upsert、去重機制
- 驗收:與 MSP Router 端到端通;重複投遞下游無重複效果;deadline 超時整鏈中止

### Phase D — 發布管理與觀測
- 產出:§9 metrics/traces/alerts、canary runbook(§8.2)、blue/green runbook(§8.4)
- 驗收:rolling 演練零丟失;canary 按 instance 比例分擔可觀測;DLQ 告警觸發

### 給 Claude Code 的通用要求

同 MSP-SPEC-001 §11:CLAUDE.md、breaking change 標註、驗收即自動化測試、缺口記 SPEC-GAP 不腦補。

## 12. 決策記錄與開放問題

### 12.1 已決議

| # | 決議 |
|---|---|
| D1 | Input 走 NATS **JetStream**(非 core NATS);pull durable consumer |
| D2 | MYSVC 單一部署,不隨 MSP blue/green 複製(MSP-SPEC-001 §2.3) |
| D3 | MYSVC → Router 用 gRPC(MSP Q6);Java/SpringBoot,grpc-java |
| D4 | At-least-once + 冪等;explicit ack,處理完成才 ack |
| D5 | 版本相依前/後處理不得存在於 MYSVC(歸 model image) |

### 12.2 開放問題

| # | 問題 | 影響 |
|---|---|---|
| OQ-1 | 同 device 的資料是否需嚴格順序處理?(需製程端確認) | §6 派發模型、重投策略 |
| OQ-2 | 既有 subject 結構是否含可提取的 device id | §3.4、§8.2 canary 指派 |
| OQ-3 | 下游寫入目標現況(topic/DB)與可否 upsert | §7 去重機制選型 |
