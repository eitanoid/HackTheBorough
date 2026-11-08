"use client";

import { type FormEvent, useEffect, useRef, useState } from "react";
import {
  createSource,
  getSource,
  listSources,
  pollSource,
  type Source,
  type SourceKind,
} from "@/lib/source-api";
import { getAnduinClient } from "@/lib/anduin-client";
import ConfirmDeleteDialog from "@/components/confirm-delete-dialog";

const maxCSVBytes = 20 * 1024 * 1024;

const defaultSchema = JSON.stringify(
  {
    type: "object",
    properties: {
      date: { type: "string", description: "When the record was observed" },
      title: { type: "string" },
      value: { type: "number" },
    },
  },
  null,
  2,
);

function normalizedKind(kind: string): SourceKind {
  const normalized = kind.toLowerCase();
  if (normalized.includes("rss")) return "rss";
  if (normalized.includes("web") || normalized.includes("http_json")) return "web";
  return "csv";
}

function kindLabel(kind: string) {
  switch (normalizedKind(kind)) {
    case "rss":
      return "RSS feed";
    case "web":
      return "Web source";
    default:
      return "CSV source";
  }
}

function kindBadge(kind: string) {
  switch (normalizedKind(kind)) {
    case "rss":
      return "RSS";
    case "web":
      return "WEB";
    default:
      return "CSV";
  }
}

function sourceUrl(source: Source) {
  if (source.config && typeof source.config === "object") {
    return source.config.url ?? "";
  }
  if (typeof source.config === "string") {
    try {
      const config = JSON.parse(source.config) as { url?: string };
      return config.url ?? "";
    } catch {
      return "";
    }
  }
  return "";
}

function recordCount(source: Source) {
  const value = source.record_count ?? source.recordCount ?? 0;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

function lastError(source: Source) {
  return source.last_error ?? source.lastError ?? "";
}

function lastPolled(source: Source) {
  return source.last_polled_at ?? source.lastPolledAt ?? null;
}

function interval(source: Source) {
  return source.poll_interval_s ?? source.pollIntervalSeconds ?? null;
}

function createdAt(source: Source) {
  const value = source.created_at ?? source.createdAt;
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString();
}

function formatFileSize(bytes: number) {
  return bytes >= 1024 * 1024
    ? `${(bytes / (1024 * 1024)).toFixed(1)} MB`
    : `${Math.max(1, Math.round(bytes / 1024))} KB`;
}

export default function SourceManager() {
  const [sources, setSources] = useState<Source[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [showCreate, setShowCreate] = useState(false);
  const [selectedId, setSelectedId] = useState("");
  const [selectedSource, setSelectedSource] = useState<Source | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState("");
  const [polling, setPolling] = useState(false);
  const [pollNotice, setPollNotice] = useState("");
  const [pollError, setPollError] = useState("");
  const [deletingSourceId, setDeletingSourceId] = useState("");
  const [deleteError, setDeleteError] = useState("");
  const [sourceToDelete, setSourceToDelete] = useState<Source | null>(null);

  async function refreshSources(signal?: AbortSignal) {
    setLoadError("");
    try {
      const rows = await listSources(signal);
      setSources(rows);
    } catch (error) {
      if (error instanceof Error && error.name === "AbortError") return;
      setLoadError(error instanceof Error ? error.message : "Could not load sources.");
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }

  useEffect(() => {
    const controller = new AbortController();
    void listSources(controller.signal)
      .then((rows) => {
        setSources(rows);
        setLoadError("");
      })
      .catch((error: unknown) => {
        if (error instanceof Error && error.name === "AbortError") return;
        setLoadError(error instanceof Error ? error.message : "Could not load sources.");
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    if (!selectedId) return;
    const controller = new AbortController();
    void getSource(selectedId, controller.signal)
      .then(setSelectedSource)
      .catch((error: unknown) => {
        if (error instanceof Error && error.name === "AbortError") return;
        setDetailError(error instanceof Error ? error.message : "Could not load source details.");
      })
      .finally(() => {
        if (!controller.signal.aborted) setDetailLoading(false);
      });
    return () => controller.abort();
  }, [selectedId]);

  async function handleCreate(input: {
    name: string;
    kind: SourceKind;
    csvMode: "file" | "url";
    url: string;
    pollIntervalSeconds: number;
    query: string;
    schema: string;
    timeField: string;
    file?: File;
  }) {
    let created: Source;
    if (input.kind === "csv" && input.csvMode === "file") {
      if (!input.file) throw new Error("Choose a CSV file to upload.");
      const uploaded = await getAnduinClient().uploadCSVSource({
        name: input.name.trim(),
        fileName: input.file.name,
        csvData: new Uint8Array(await input.file.arrayBuffer()),
      });
      if (!uploaded.source) throw new Error("The API returned no uploaded source.");
      created = await getSource(uploaded.source.id);
    } else {
      let config: Record<string, unknown>;
      if (input.kind === "web") {
        let schema: unknown;
        try {
          schema = JSON.parse(input.schema);
        } catch {
          throw new Error("The extraction schema must be valid JSON.");
        }
        if (!schema || typeof schema !== "object" || Array.isArray(schema)) {
          throw new Error("The extraction schema must be a JSON object.");
        }
        config = { url: input.url, query: input.query, schema };
      } else {
        config = { url: input.url };
        if (input.kind === "csv" && input.timeField.trim()) {
          config.time_field = input.timeField.trim();
        }
      }

      created = await createSource({
        name: input.name.trim(),
        kind: input.kind,
        config,
        pollIntervalSeconds: input.pollIntervalSeconds,
      });
    }

    setShowCreate(false);
    setLoading(true);
    await refreshSources();
    setSelectedSource(created);
    setDetailLoading(true);
    setDetailError("");
    setPollNotice("");
    setPollError("");
    setSelectedId(created.id);
  }

  async function pollSelected() {
    if (!selectedId || polling) return;
    setPolling(true);
    setPollNotice("");
    setPollError("");
    try {
      const { inserted } = await pollSource(selectedId);
      setPollNotice(
        inserted === 0
          ? "Poll finished. No new records."
          : `Poll finished. ${inserted} new ${inserted === 1 ? "record" : "records"} stored.`,
      );
      try {
        const [rows, fresh] = await Promise.all([listSources(), getSource(selectedId)]);
        setSources(rows);
        setSelectedSource(fresh);
      } catch {
        // The poll succeeded; refreshing the cached view is best-effort.
      }
    } catch (error) {
      setPollError(error instanceof Error ? error.message : "Could not poll this source.");
    } finally {
      setPolling(false);
    }
  }

  async function deleteSource() {
    const source = sourceToDelete;
    if (!source) return;
    setDeletingSourceId(source.id);
    setDeleteError("");
    try {
      const response = await getAnduinClient().deleteSource({ id: source.id });
      setSources((current) => current.filter((item) => item.id !== source.id));
      if (selectedId === source.id) {
        setSelectedId("");
        setSelectedSource(null);
      }
      setSourceToDelete(null);
      if (response.affectedProjectIds.length > 0) {
        window.dispatchEvent(new CustomEvent("anduin:source-deleted", {
          detail: response.affectedProjectIds,
        }));
      }
    } catch (error) {
      setDeleteError(error instanceof Error ? error.message : "Could not delete this source.");
    } finally {
      setDeletingSourceId("");
    }
  }

  function openSource(source: Source) {
    setSelectedSource(source);
    setDetailLoading(true);
    setDetailError("");
    setPollNotice("");
    setPollError("");
    setSelectedId(source.id);
  }

  return (
    <section className="source-row" id="sources" aria-labelledby="sources-heading">
      <div className="source-row-heading">
        <div>
          <span className="eyebrow">CONNECTED SOURCES</span>
          <h2 id="sources-heading">Your data, connected</h2>
        </div>
        <div className="source-actions">
          <button
            type="button"
            className="button button-primary source-add-button"
            onClick={() => setShowCreate(true)}
          >
            <span aria-hidden="true">＋</span> Add source
          </button>
          <button
            type="button"
            className="text-link source-refresh"
            onClick={() => {
              setLoading(true);
              void refreshSources();
            }}
            disabled={loading}
          >
            Refresh <span aria-hidden="true">↻</span>
          </button>
        </div>
      </div>

      {loadError && (
        <div className="source-notice source-notice-error" role="alert">
          <span>{loadError}</span>
          <button type="button" onClick={() => void refreshSources()}>Try again</button>
        </div>
      )}
      {deleteError && !sourceToDelete && <div className="source-notice source-notice-error" role="alert">{deleteError}</div>}

      {loading ? (
        <div className="source-list" aria-label="Loading sources">
          {[0, 1, 2].map((item) => <div className="source-loading" key={item} />)}
        </div>
      ) : sources.length === 0 ? (
        <div className="source-empty">
          <span className="source-empty-mark" aria-hidden="true">＋</span>
          <div>
            <strong>No sources connected yet</strong>
            <span>Add an RSS feed, a CSV URL or file, or a web page to get started.</span>
          </div>
          <button type="button" className="text-link" onClick={() => setShowCreate(true)}>
            Connect your first source <span aria-hidden="true">→</span>
          </button>
        </div>
      ) : (
        <div className="source-list" role="list" aria-label="Connected sources">
          {sources.map((source) => {
            const failed = Boolean(lastError(source));
            return (
              <div className="source-item source-item-row" key={source.id} role="listitem">
                <button
                  type="button"
                  className="source-item-select"
                  onClick={() => openSource(source)}
                  aria-label={`View ${source.name}, ${kindLabel(source.kind)}`}
                >
                  <span className={`source-icon source-${normalizedKind(source.kind)}`}>
                    {kindBadge(source.kind)}
                  </span>
                  <span className="source-item-copy">
                    <strong>{source.name}</strong>
                    <small>
                      {recordCount(source).toLocaleString()} records
                      {interval(source) ? ` · checks every ${interval(source)}s` : " · one-off source"}
                    </small>
                  </span>
                  <span className={`source-state ${failed ? "source-state-error" : ""}`}>
                    <i aria-hidden="true" />{failed ? "Issue" : "Connected"}
                  </span>
                </button>
                <button
                  type="button"
                  className="source-delete-button"
                  aria-label={`Delete ${source.name}`}
                  title={`Delete ${source.name}`}
                  disabled={Boolean(deletingSourceId)}
                  onClick={() => {
                    setDeleteError("");
                    setSourceToDelete(source);
                  }}
                >
                  {deletingSourceId === source.id ? "…" : "×"}
                </button>
              </div>
            );
          })}
        </div>
      )}

      <p className="source-list-footnote">
        {sources.length > 0 && `${sources.length} ${sources.length === 1 ? "source" : "sources"} connected`}
        {sources.length > 0 && <span> · </span>}
        Select a source to view its connection details.
      </p>

      {showCreate && (
        <CreateSourceDialog onClose={() => setShowCreate(false)} onCreate={handleCreate} />
      )}

      {selectedId && (
        <SourceDetailDialog
          source={selectedSource}
          loading={detailLoading}
          error={detailError}
          polling={polling}
          pollNotice={pollNotice}
          pollError={pollError}
          onPoll={() => void pollSelected()}
          onClose={() => {
            setSelectedId("");
            setSelectedSource(null);
            setPollNotice("");
            setPollError("");
          }}
        />
      )}
      <ConfirmDeleteDialog
        open={Boolean(sourceToDelete)}
        itemName={sourceToDelete?.name ?? ""}
        itemType="source"
        impact="Its stored records will be removed. Any linked projects will lose their current workflow results and need to rebuild their analysis."
        busy={Boolean(sourceToDelete && deletingSourceId === sourceToDelete.id)}
        error={deleteError}
        onCancel={() => {
          if (!deletingSourceId) setSourceToDelete(null);
        }}
        onConfirm={() => void deleteSource()}
      />
    </section>
  );
}

function CreateSourceDialog({
  onClose,
  onCreate,
}: {
  onClose: () => void;
  onCreate: (input: {
    name: string;
    kind: SourceKind;
    csvMode: "file" | "url";
    url: string;
    pollIntervalSeconds: number;
    query: string;
    schema: string;
    timeField: string;
    file?: File;
  }) => Promise<void>;
}) {
  const [kind, setKind] = useState<SourceKind>("rss");
  const [csvMode, setCsvMode] = useState<"file" | "url">("file");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [draggingFile, setDraggingFile] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  function chooseFile(file?: File) {
    if (!file) return;
    if (!file.name.toLowerCase().endsWith(".csv")) {
      setSelectedFile(null);
      setError("Choose a file with a .csv extension.");
      return;
    }
    if (file.size === 0) {
      setSelectedFile(null);
      setError("The selected CSV file is empty.");
      return;
    }
    if (file.size > maxCSVBytes) {
      setSelectedFile(null);
      setError(`CSV files must be 20 MB or smaller.`);
      return;
    }
    setSelectedFile(file);
    setError("");
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    const form = new FormData(event.currentTarget);
    const file = kind === "csv" && csvMode === "file" ? selectedFile ?? undefined : undefined;
    if (kind === "csv" && csvMode === "file" && !file) {
      setError("Choose a CSV file to upload.");
      return;
    }
    setBusy(true);
    try {
      await onCreate({
        name: String(form.get("name") ?? ""),
        kind,
        csvMode,
        url: String(form.get("url") ?? ""),
        pollIntervalSeconds: Number(form.get("pollIntervalSeconds") ?? 300),
        query: String(form.get("query") ?? ""),
        schema: String(form.get("schema") ?? ""),
        timeField: String(form.get("timeField") ?? ""),
        file,
      });
    } catch (createError) {
      setError(createError instanceof Error ? createError.message : "Could not create this source.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="source-dialog-backdrop" onMouseDown={(event) => {
      if (event.target === event.currentTarget && !busy) onClose();
    }}>
      <section className="source-dialog" role="dialog" aria-modal="true" aria-labelledby="create-source-heading">
        <div className="source-dialog-heading">
          <div>
            <span className="eyebrow">NEW CONNECTION</span>
            <h2 id="create-source-heading">Add a data source</h2>
            <p>
              {kind === "csv"
                ? csvMode === "file"
                  ? "Drop in a CSV file to import its rows once, or switch to a URL for scheduled updates."
                  : "Connect a public CSV URL and choose how often to check it for new rows."
                : "Connect a feed or URL and we’ll keep its records up to date."}
            </p>
          </div>
          <button type="button" className="source-dialog-close" onClick={onClose} aria-label="Close dialog" disabled={busy}>×</button>
        </div>

        <form onSubmit={submit} className="source-form">
          <label className="source-field">
            <span>Source name</span>
            <input name="name" required maxLength={120} placeholder="e.g. Hackney air quality" autoFocus />
          </label>

          <label className="source-field">
            <span>Source type</span>
            <select value={kind} onChange={(event) => setKind(event.target.value as SourceKind)}>
              <option value="rss">RSS feed</option>
              <option value="csv">CSV</option>
              <option value="web">Web page</option>
            </select>
          </label>

          {kind === "csv" && (
            <div className="csv-source-mode" role="group" aria-label="How to connect the CSV">
              <button
                type="button"
                className={csvMode === "file" ? "is-selected" : ""}
                aria-pressed={csvMode === "file"}
                onClick={() => setCsvMode("file")}
                disabled={busy}
              >
                Upload a file
              </button>
              <button
                type="button"
                className={csvMode === "url" ? "is-selected" : ""}
                aria-pressed={csvMode === "url"}
                onClick={() => setCsvMode("url")}
                disabled={busy}
              >
                Use a URL
              </button>
            </div>
          )}

          {kind === "csv" && csvMode === "file" ? (
            <div
              className={`csv-dropzone ${draggingFile ? "is-dragging" : ""} ${selectedFile ? "has-file" : ""}`}
              onDragOver={(event) => {
                event.preventDefault();
                setDraggingFile(true);
              }}
              onDragLeave={() => setDraggingFile(false)}
              onDrop={(event) => {
                event.preventDefault();
                setDraggingFile(false);
                chooseFile(event.dataTransfer.files[0]);
              }}
            >
              <input
                ref={fileInputRef}
                className="csv-file-input"
                type="file"
                accept=".csv,text/csv"
                aria-label="Choose a CSV file"
                onChange={(event) => {
                  chooseFile(event.currentTarget.files?.[0]);
                  event.currentTarget.value = "";
                }}
              />
              {selectedFile ? (
                <div className="csv-selected-file">
                  <span className="source-icon source-csv">CSV</span>
                  <span><strong>{selectedFile.name}</strong><small>{formatFileSize(selectedFile.size)}</small></span>
                  <button type="button" className="csv-remove-file" onClick={() => setSelectedFile(null)} disabled={busy}>Remove</button>
                </div>
              ) : (
                <div className="csv-dropzone-prompt">
                  <span className="csv-upload-icon" aria-hidden="true">↑</span>
                  <strong>Drop your CSV file here</strong>
                  <span>or</span>
                  <button type="button" className="csv-browse-button" onClick={() => fileInputRef.current?.click()} disabled={busy}>Browse files</button>
                  <small>CSV format · up to 20 MB</small>
                </div>
              )}
            </div>
          ) : (
            <label className="source-field">
              <span>{kind === "rss" ? "Feed URL" : kind === "csv" ? "CSV URL" : "Page URL"}</span>
              <input
                name="url"
                type="url"
                required
                placeholder={kind === "rss" ? "https://example.com/feed.xml" : kind === "csv" ? "https://example.com/data.csv" : "https://example.com/data"}
              />
            </label>
          )}

          {kind === "web" && (
            <>
              <label className="source-field">
                <span>What should we extract?</span>
                <input name="query" placeholder="Extract the latest public records" />
              </label>
              <label className="source-field">
                <span>Record schema (JSON)</span>
                <textarea name="schema" required defaultValue={defaultSchema} rows={7} spellCheck={false} />
                <small>Describe the shape of one extracted record.</small>
              </label>
            </>
          )}

          {kind === "csv" && csvMode === "url" && (
            <label className="source-field">
              <span>Timestamp column <em>Optional</em></span>
              <input name="timeField" placeholder="date" />
              <small>Leave blank to detect a common date or time column.</small>
            </label>
          )}

          {(kind !== "csv" || csvMode === "url") && (
            <label className="source-field">
              <span>Refresh interval</span>
              <select name="pollIntervalSeconds" defaultValue="300">
                <option value="300">Every 5 minutes</option>
                <option value="900">Every 15 minutes</option>
                <option value="3600">Every hour</option>
                <option value="86400">Every 24 hours</option>
              </select>
            </label>
          )}

          {error && <p className="source-form-error" role="alert">{error}</p>}

          <div className="source-dialog-actions">
            <button type="button" className="button button-outline" onClick={onClose} disabled={busy}>Cancel</button>
            <button type="submit" className="button button-primary" disabled={busy}>
              {busy ? <><i className="source-spinner" /> {kind === "csv" ? "Uploading…" : "Connecting…"}</> : kind === "csv" ? "Upload CSV" : "Connect source"}
            </button>
          </div>
        </form>
      </section>
    </div>
  );
}

function SourceDetailDialog({
  source,
  loading,
  error,
  polling,
  pollNotice,
  pollError,
  onPoll,
  onClose,
}: {
  source: Source | null;
  loading: boolean;
  error: string;
  polling: boolean;
  pollNotice: string;
  pollError: string;
  onPoll: () => void;
  onClose: () => void;
}) {
  const failed = source ? Boolean(lastError(source)) : false;
  return (
    <div className="source-dialog-backdrop" onMouseDown={(event) => {
      if (event.target === event.currentTarget && !polling) onClose();
    }}>
      <section className="source-dialog source-detail-dialog" role="dialog" aria-modal="true" aria-labelledby="source-detail-heading">
        <div className="source-dialog-heading">
          <div>
            <span className="eyebrow">SOURCE DETAILS</span>
            <h2 id="source-detail-heading">{source?.name ?? "Loading source"}</h2>
            <p>Connection status and ingestion details.</p>
          </div>
          <button type="button" className="source-dialog-close" onClick={onClose} aria-label="Close dialog" disabled={polling}>×</button>
        </div>

        {loading && !source ? (
          <div className="source-detail-loading"><i className="source-spinner" /> Loading source details…</div>
        ) : error ? (
          <p className="source-form-error" role="alert">{error}</p>
        ) : source ? (
          <>
            <div className="source-detail-status">
              <span className={`source-icon source-${normalizedKind(source.kind)}`}>{kindBadge(source.kind)}</span>
              <span><strong>{kindLabel(source.kind)}</strong><small>{sourceUrl(source) || (typeof source.config === "object" ? source.config?.file_name : "") || "Source connection"}</small></span>
              <span className={`source-state ${failed ? "source-state-error" : ""}`}><i aria-hidden="true" />{failed ? "Issue" : "Connected"}</span>
            </div>
            {failed && <p className="source-detail-error">{lastError(source)}</p>}
            <dl className="source-detail-grid">
              <div><dt>Records stored</dt><dd>{recordCount(source).toLocaleString()}</dd></div>
              <div><dt>Refresh interval</dt><dd>{interval(source) ? `${interval(source)} seconds` : "One-off"}</dd></div>
              <div><dt>Last checked</dt><dd>{lastPolled(source) ? new Date(lastPolled(source) as string).toLocaleString() : "Not checked yet"}</dd></div>
              <div><dt>Added</dt><dd>{createdAt(source)}</dd></div>
            </dl>
            {source.profile?.summary && (
              <div className="source-profile-summary">
                <span className="eyebrow">DATA PROFILE</span>
                <p>{source.profile.summary}</p>
                {!!source.profile.fields?.length && (
                  <ul>
                    {source.profile.fields.slice(0, 6).map((field) => (
                      <li key={field.path}><code>{field.path}</code><span>{field.role}</span></li>
                    ))}
                  </ul>
                )}
              </div>
            )}
          </>
        ) : null}

        {pollError && <p className="source-poll-status source-poll-fail" role="alert">{pollError}</p>}
        {pollNotice && <p className="source-poll-status source-poll-ok" role="status">{pollNotice}</p>}

        <div className="source-dialog-actions">
          <button type="button" className="button button-primary" onClick={onPoll} disabled={polling || loading}>
            {polling ? <><i className="source-spinner" /> Polling…</> : "Poll now"}
          </button>
          <button type="button" className="button button-outline" onClick={onClose} disabled={polling}>Close</button>
        </div>
      </section>
    </div>
  );
}
