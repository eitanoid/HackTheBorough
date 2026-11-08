import { getAnduinClient } from "./anduin-client";

export type SourceKind = "rss" | "web" | "csv";

/** How the most recent backfill walked through the source's pages. */
export type BackfillReport = {
  pages?: number;
  methods?: Record<string, number>;
  rejected_probes?: number;
  rows?: number;
  stopped?: string;
  finished_at?: string;
};

export type SourceProfile = {
  summary?: string;
  timestamp_path?: string | null;
  fields?: Array<{
    path: string;
    role: string;
    description?: string;
    unit?: string | null;
  }>;
};

export type Source = {
  id: string;
  name: string;
  kind: string;
  config?: { url?: string; file_name?: string } | string | null;
  profile?: SourceProfile | null;
  record_count?: number | string;
  recordCount?: number | string;
  poll_interval_s?: number | null;
  pollIntervalSeconds?: number;
  last_polled_at?: string | null;
  lastPolledAt?: string;
  last_error?: string | null;
  lastError?: string;
  created_at?: string;
  createdAt?: string;
  backfill_rows?: number;
  backfillRows?: number;
  backfill_pending?: boolean;
  backfillPending?: boolean;
  last_backfill?: BackfillReport | null;
  lastBackfill?: BackfillReport | null;
};

type SourceListResponse = { sources?: Source[] };

async function parseResponse<T>(response: Response): Promise<T> {
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    const message =
      typeof body.error === "string"
        ? body.error
        : `Request failed (${response.status})`;
    throw new Error(message);
  }
  return body as T;
}

export async function listSources(signal?: AbortSignal): Promise<Source[]> {
  const response = await fetch("/api/sources", {
    method: "GET",
    cache: "no-store",
    signal,
  });
  const body = await parseResponse<SourceListResponse>(response);
  return body.sources ?? [];
}

export async function getSource(id: string, signal?: AbortSignal): Promise<Source> {
  const response = await fetch(`/api/sources/${encodeURIComponent(id)}`, {
    method: "GET",
    cache: "no-store",
    signal,
  });
  return parseResponse<Source>(response);
}

/**
 * inserted rows, or queued=true when the poll will run in the background
 * (backfill=true when it is a pending backfill handed to the ingest loop).
 */
export type PollResult = { inserted: number; queued?: boolean; backfill?: boolean };

export async function pollSource(id: string): Promise<PollResult> {
  const response = await fetch(`/api/sources/${encodeURIComponent(id)}/poll`, {
    method: "POST",
    cache: "no-store",
  });
  return parseResponse<PollResult>(response);
}

export async function createSource(input: {
  name: string;
  kind: SourceKind;
  config: Record<string, unknown>;
  pollIntervalSeconds: number;
  /** Omit to use the server's default backfill target. */
  backfillRows?: number;
}): Promise<Source> {
  const response = await fetch("/api/sources", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      name: input.name,
      kind: input.kind,
      config: input.config,
      poll_interval_s: input.pollIntervalSeconds,
      ...(input.backfillRows ? { backfill_rows: input.backfillRows } : {}),
    }),
  });
  return parseResponse<Source>(response);
}

/**
 * Changes a source's backfill target. Raising it above the stored row count
 * queues a re-backfill in the background; lowering it never deletes rows.
 */
export async function updateSourceBackfill(
  id: string,
  rows: number,
): Promise<{ backfillScheduled: boolean }> {
  const response = await getAnduinClient().updateSource({ id, backfillRows: rows });
  return { backfillScheduled: response.backfillScheduled };
}
