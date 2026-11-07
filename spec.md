# Technical Specification: Dynamic Data Analysis & Insight Pipeline Engine

## 1. System Overview & Overarching Concept

The platform is an autonomous, schema-adaptive data processing and insight engine designed to turn heterogeneous, dynamic data streams (e.g., social feeds, financial tickers, macro-economic RSS, custom REST APIs) into continuous statistical analysis and structured agentic insights.

The core architectural objective is to cleanly separate **deterministic data processing** from **heuristic AI synthesis**:

* **Ingestion & Data Store:** Stream sources are ingested continuously into isolated, schema-flexible storage using PostgreSQL `JSONB`.
* **Dynamic Workflow Generation:** An orchestration agent inspects user-defined intent (goals) alongside target data schemas to generate deterministic statistical analysis scripts (Python/Pandas) and structured LLM insight prompts.
* **Deterministic Execution & AI Synthesis:** A dedicated worker sandbox executes numerical statistical scripts without hallucination risk, while downstream AI agents rank, filter noise, and generate tailored insights.
* **Adaptive Feedback Loop:** The system continuously monitors for schema drift or invalidation events, triggering workflow updates only when underlying data structures change significantly or insight validity expires.

---

## 2. System Architecture & Component Interactions

```
┌────────────────────────────────────────────────────────────────────────┐
│                          INGESTION SERVICE LAYER                       │
│  ┌──────────────────────┐   ┌──────────────────┐   ┌─────────────────┐ │
│  │ Remote Data Source 1 │   │ Remote Source 2  │   │ Remote Source N │ │
│  └──────────┬───────────┘   └────────┬─────────┘   └────────┬────────┘ │
│             │                        │                      │          │
│             └──────────────────┐     │     ┌────────────────┘          │
│                                ▼     ▼     ▼                           │
│                          ┌────────────────────┐                        │
│                          │ Generic Ingestor   │                        │
│                          └─────────┬──────────┘                        │
└────────────────────────────────────┼───────────────────────────────────┘
                                     │ (Write Raw JSONB Payload)
                                     ▼
┌────────────────────────────────────────────────────────────────────────┐
│                              DATA STORE                                │
│        ┌──────────────────┐             ┌─────────────────────┐        │
│        │  data_sources    │             │      raw_data       │        │
│        └──────────────────┘             └─────────────────────┘        │
└────────────────────────────────────┬───────────────────────────────────┘
                                     │ (Notify/Poll Stream Changes)
                                     ▼
┌────────────────────────────────────────────────────────────────────────┐
│                         WORKFLOW & PIPELINE LAYER                      │
│                                                                        │
│                       ┌─────────────────────────┐                      │
│                       │    Workflow Manager     │                      │
│                       └────────────┬────────────┘                      │
│                                    │                                   │
│            ┌───────────────────────┴───────────────────────┐           │
│            ▼                                               ▼           │
│  ┌──────────────────┐                            ┌──────────────────┐  │
│  │ Workflow Creator │                            │ Drift / Schema   │  │
│  │ (LLM Code Gen)   │                            │ Validator        │  │
│  └────────┬─────────┘                            └──────────────────┘  │
│           │ (Persist Executable Artifacts)                             │
│           ▼                                                            │
│  ┌──────────────────┐                                                  │
│  │   PROJECT STORE  │                                                  │
│  │ (projects,       │                                                  │
│  │  workflows,      │                                                  │
│  │  insights)       │                                                  │
│  └────────┬─────────┘                                                  │
└───────────┼────────────────────────────────────────────────────────────┘
            │
            ▼
┌────────────────────────────────────────────────────────────────────────┐
│                       EXECUTION & SYNTHESIS LAYER                      │
│                                                                        │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │                    Execution Worker Sandbox                      │  │
│  │  ┌─────────────────────────────┐    ┌──────────────────────────┐ │  │
│  │  │ Statistical Analysis Engine │    │  AI Signal Ranker &      │ │  │
│  │  │ (Deterministic Python Exec) │───>│  Synthesis Agent         │ │  │
│  │  └─────────────────────────────┘    └─────────────┬────────────┘ │  │
│  └───────────────────────────────────────────────────┼──────────────┘  │
└──────────────────────────────────────────────────────┼─────────────────┘
                                                       │
                                                       ▼
┌────────────────────────────────────────────────────────────────────────┐
│                       PRESENTATION & UI LAYER                          │
│                                                                        │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │                     AI Reporter & Visualizer                     │  │
│  │  (Interactive Graphs, Executive Reports & Direct Insights)       │  │
│  └──────────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────────┘

```

---

## 3. Database Schema Specification (PostgreSQL)

The database design relies on two core schemas: **Data Store** (handling remote sources and raw payloads) and **Project Store** (handling user intents, compiled code workflows, generated statistics, and AI insights).

### 3.1. Data Store Tables

#### `data_sources`

Registers all external sources, their endpoints, ingestion rules, and active schemas.

* **`id`** (`UUID`, PRIMARY KEY, `DEFAULT gen_random_uuid()`)
* **`name`** (`TEXT`, NOT NULL): Human-readable name of the source (e.g., "Twitter/X Feed - Oil Prices").
* **`ds_contact_url`** (`TEXT`, NOT NULL): Target REST, RSS, or WebSocket API endpoint.
* **`polling_interval`** (`INTEGER`, NOT NULL): Fetch interval measured in seconds.
* **`data_schema`** (`JSONB`, NOT NULL): Expected JSON structural schema, unit types, and keys.
* **`created_at`** (`TIMESTAMPTZ`, DEFAULT `CURRENT_TIMESTAMP`)
* **`updated_at`** (`TIMESTAMPTZ`, DEFAULT `CURRENT_TIMESTAMP`)

#### `raw_data`

Stores ingested payloads linked directly to their originating source.

* **`id`** (`UUID`, PRIMARY KEY, `DEFAULT gen_random_uuid()`)
* **`ds_uuid`** (`UUID`, NOT NULL, FOREIGN KEY $\rightarrow$ `data_sources(id)` ON DELETE CASCADE)
* **`created_at`** (`TIMESTAMPTZ`, NOT NULL, DEFAULT `CURRENT_TIMESTAMP`)
* **`payload`** (`JSONB`, NOT NULL): Structured schema-compliant JSON data envelope.

---

### 3.2. Project Store Tables

#### `projects`

Encapsulates high-level insight goals, linked data sources, and cached outputs.

* **`id`** (`UUID`, PRIMARY KEY, `DEFAULT gen_random_uuid()`)
* **`name_goal`** (`TEXT`, NOT NULL): Detailed definition of the analytical objective.
* **`datasource_uuids`** (`UUID[]`, NOT NULL): Array of linked `data_sources(id)` records.
* **`latest_statistical_report`** (`JSONB`, NULLABLE): Latest numerical execution output payload.
* **`latest_ai_insights`** (`JSONB`, NULLABLE): Latest synthesized insight JSON payload.
* **`polling_rate`** (`INTEGER`, NOT NULL): Re-analysis schedule interval in seconds.
* **`created_at`** (`TIMESTAMPTZ`, DEFAULT `CURRENT_TIMESTAMP`)
* **`updated_at`** (`TIMESTAMPTZ`, DEFAULT `CURRENT_TIMESTAMP`)

#### `workflows`

Holds the compiled deterministic code scripts and LLM prompts associated with a project.

* **`id`** (`UUID`, PRIMARY KEY, `DEFAULT gen_random_uuid()`)
* **`project_uuid`** (`UUID`, NOT NULL, FOREIGN KEY $\rightarrow$ `projects(id)` ON DELETE CASCADE)
* **`created_at`** (`TIMESTAMPTZ`, NOT NULL, DEFAULT `CURRENT_TIMESTAMP`)
* **`statistics_code`** (`TEXT`, NOT NULL): Python execution script generated by the Workflow Creator Agent.
* **`ai_insights_prompt`** (`TEXT`, NOT NULL): Dynamic prompt specification for the AI Analysis/Ranker Agent.
* **`is_active`** (`BOOLEAN`, DEFAULT `TRUE`)

#### `insights`

Historical ledger of all generated insights and statistical outputs.

* **`id`** (`UUID`, PRIMARY KEY, `DEFAULT gen_random_uuid()`)
* **`project_uuid`** (`UUID`, NOT NULL, FOREIGN KEY $\rightarrow$ `projects(id)` ON DELETE CASCADE)
* **`created_at`** (`TIMESTAMPTZ`, NOT NULL, DEFAULT `CURRENT_TIMESTAMP`)
* **`relevant_up_until`** (`TIMESTAMPTZ`, NOT NULL): Expiration timestamp for the current insight validity.
* **`type`** (`VARCHAR(50)`, NOT NULL): Category tag (`STATISTICAL`, `AI_FINDING`, `COMPOSITE_ALERT`).
* **`data`** (`JSONB`, NOT NULL): Full payload containing structured text, statistical metrics, and visualization parameters.

---

## 4. Microservices & Platform Components

### 4.1. Generic Data Ingestor Service

* **Responsibility:** Ingests external datasets asynchronously based on stored configuration rules.
* **Operations:**
1. Polls endpoints according to `data_sources.polling_interval`.
2. Parses response content (JSON, RSS XML, CSV).
3. Validates unit types and timestamp fields.
4. Appends incoming rows directly into `raw_data` with the corresponding `ds_uuid`.
5. Publishes a `DataIngestedEvent` to the internal message broker.



### 4.2. Workflow Manager & Agentic Orchestrator

* **Responsibility:** Manages lifecycle, creation, validation, and invalidation of project workflows.
* **Sub-Components:**
* **Workflow Creator Agent (LLM):** Ingests project intent (`name_goal`) and linked schemas (`data_sources.data_schema`). Synthesizes:
* Deterministic Python execution code utilizing pre-approved dependencies (`pandas`, `numpy`, `scipy`).
* Structured prompts for downstream signal extraction.


* **Schema & Drift Validator:** Listens for `DataIngestedEvent`. Computes structural divergence against `data_schema`. If divergence exceeds tolerance threshold ($>15\%$ schema change), marks active workflow as invalid (`is_active = FALSE`) and triggers workflow re-generation.



### 4.3. Execution & Synthesis Engine (Stat Engine & AI Ranker)

* **Responsibility:** Computes statistical metrics and transforms raw outputs into structured insights.
* **Sub-Components:**
* **Statistical Analysis Engine (Sandbox):** Isolated container execution environment. Receives target `raw_data` slice and `workflows.statistics_code`. Executes code, extracts analytical outputs (means, correlations, anomaly scores, variance), and writes output to `projects.latest_statistical_report`.
* **AI Signal Ranker & Synthesis Agent:** Consumes `latest_statistical_report` combined with raw payload context using `workflows.ai_insights_prompt`. Performs noise filtering, ranks anomalies by relevance, generates structured JSON recommendations, and stores records in `insights`.



### 4.4. AI Reporter & Visualizer Service

* **Responsibility:** Serves the frontend rendering engine with dynamic visualization blueprints.
* **Operations:**
* Exposes GraphQL/REST APIs for user dashboards.
* Translates `insights.data` payloads into standard UI chart definitions (Time-Series, Sankey Flow Diagrams, Anomaly Heatmaps, Correlation Matrices).
* Serves automated text summary reports grouped by user interest and intent urgency.



---

## 5. Integration Contracts & Data Flows

### 5.1. Data Ingestion Contract

```json
{
  "source_uuid": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "ingested_at": "2026-11-07T15:18:00Z",
  "payload": {
    "timestamp": 1794064680,
    "metric_name": "inflation_index",
    "value": 3.42,
    "metadata": {
      "region": "UK",
      "category": "energy"
    }
  }
}

```

### 5.2. Workflow Generation Output Specification

The Workflow Creator Agent must return a structured payload adhering to this spec:

```json
{
  "project_uuid": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "statistics_code": "import pandas as pd\ndef process(df):\n    df['rolling_avg'] = df['value'].rolling(window=5).mean()\n    return {'rolling_avg': df['rolling_avg'].iloc[-1]}",
  "ai_insights_prompt": "Analyze the following statistical metrics for energy price inflation. Identify key anomalies exceeding 2 standard deviations and generate action items.",
  "expected_metrics": ["rolling_avg", "std_dev", "correlation_score"]
}

```

### 5.3. Generated Insight Output Specification

```json
{
  "insight_id": "98765432-10fe-dcba-ba09-876543210fed",
  "project_uuid": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "type": "COMPOSITE_ALERT",
  "relevance_score": 0.94,
  "statistical_summary": {
    "metric": "energy_inflation",
    "current_value": 4.12,
    "baseline_mean": 2.80,
    "anomaly_sigma": 2.4
  },
  "narrative_finding": "Energy price spikes present a significant cost-of-living risk over the next quarter.",
  "recommended_actions": [
    "Rebalance utility budget allocation.",
    "Hedge short-term variable tariff exposure."
  ],
  "visualization_spec": {
    "chart_type": "HEATMAP",
    "x_axis": "region",
    "y_axis": "category",
    "value_field": "anomaly_sigma"
  }
}

```

---

## 6. Execution Safeguards & System Requirements

1. **Sandboxed Code Execution:** The Stat Engine must execute user/agent-generated code in a restricted container sandbox (e.g., gVisor, AWS Lambda, or isolated Docker instance) with network access disabled, hard execution timeouts (max 10 seconds), and restricted memory bounds (512MB limit).
2. **Deterministic Fallbacks:** If dynamic script execution fails runtime evaluation, the Workflow Manager must flag the error, fall back to a default standard summary statistics pipeline (mean, median, rolling standard deviation), and queue a workflow regeneration task.
3. **Event-Driven Messaging:** Communication between the Generic Ingestor, Workflow Manager, and Execution Worker must run over an event bus (e.g., Redis Pub/Sub, RabbitMQ, or NATS) to ensure decouple processing and horizontal scalability.
