# RAG — 检索增强生成架构

> 文件: internal/platform/rag/
> 依赖: Qdrant (向量库) + Ollama (Embedding)

## 一、架构定位

RAG (Retrieval-Augmented Generation) 为 Sales Agent 提供产品、评价、退货的语义检索能力。

```
EventBus 事件
  │
  ├─ EventProductSynced
  ├─ EventReviewSynced
  └─ EventReturnSynced
  │
  ▼
RAG Indexer (EventBus Consumer)
  │
  ├─ Chunk: 将文本切分为 chunk (MaxLen=500)
  ├─ Embed: Ollama 生成 Embedding (1024d)
  └─ Index: 写入 Qdrant
  │
  ▼
Qdrant Collection: vela_knowledge
  │
  ▼
RAG Retriever (查询时)
  │
  ├─ Embed query → 1024d vector
  ├─ Qdrant Search (ANN, top-K=10)
  ├─ Rerank (score threshold > 0.7)
  └─ 返回相关文档片段
  │
  ▼
SalesAgent Context
  注入检索结果 → LLM 生成回复
```

## 二、索引数据流

```
Indexer.Start(bus):
  订阅事件:
  
  EventProductSynced:
    Chunk: "Product: {title}. Price: {price}. Description: {desc}"
    Embed: Ollama embed(text) → []float64
    Index: Qdrant.Upsert(collection, vector, payload{shop_id, source, source_id})
  
  EventReviewSynced:
    Chunk: "Review: {rating} stars. '{body}'. Reply: '{reply}'"
    Embed + Index
  
  EventReturnSynced:
    Chunk: "Return: Order #{order}. Reason: {reason}. Note: {note}"
    Embed + Index
```

## 三、检索数据流

```
Retriever.Search(query, shopID, topK=10):
  1. Embed query
     ollama.Embed(query) → vector[1024]
  
  2. Qdrant Search
     collection.search(vector, filter={shop_id: shopID}, limit=topK)
     → [{id, score, payload}, ...]
  
  3. Rerank
     score > 0.7 → 返回
     阈值以下 → 过滤
  
  4. 返回片段
     [{content, source, source_id, score}, ...]
```

## 四、Chunk 策略

```
Chunker.MakeChunks(inputs):
  MaxLen: 500 chars
  Overlap: 50 chars
  
  产品: 1 chunk / product (title + description)
  评价: 1 chunk / review
  退货: 1 chunk / return
  
  超长内容: sliding window split
```

## 五、降级策略

```
Qdrant 不可用:
  → RAGService 返回空结果
  → Sales Agent 仅用其他 Context Injector
  → 日志: "rag: qdrant not reachable, will degrade to SQL-only"

Ollama 不可用:
  → Embedding 失败 → 跳过索引
  → 已索引数据可检索
```

## 六、关键设计决策

| 决策 | 原因 |
|------|------|
| Qdrant (非 pgvector) | 专门向量库，ANN 性能更好 |
| Ollama (本地) | 零外部 API 依赖 |
| 降级优先 | RAG 是增强，非必需 |
| EventBus 驱动索引 | 实时同步，数据变更即索引 |
