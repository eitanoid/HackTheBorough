"use client";

import { useEffect, useState, type FormEvent } from "react";
import { getAnduinClient } from "@/lib/anduin-client";
import { SourceKind, type Project, type Source } from "@/gen/hacktheburgh/v1/service_pb";

function sourceKindLabel(kind: SourceKind) {
  switch (kind) {
    case SourceKind.RSS:
      return "RSS";
    case SourceKind.WEB:
      return "Web";
    case SourceKind.CSV:
      return "CSV";
    default:
      return "Data source";
  }
}

export default function NewProjectLauncher() {
  const [open, setOpen] = useState(false);

  return (
    <>
      <button className="button button-primary new-project-button" type="button" onClick={() => setOpen(true)}>
        <span aria-hidden="true">＋</span> New project
      </button>
      {open && <NewProjectDialog onClose={() => setOpen(false)} />}
    </>
  );
}

function NewProjectDialog({ onClose }: { onClose: () => void }) {
  const [sources, setSources] = useState<Source[]>([]);
  const [selectedSourceIds, setSelectedSourceIds] = useState<string[]>([]);
  const [loadingSources, setLoadingSources] = useState(true);
  const [sourceError, setSourceError] = useState("");
  const [submitError, setSubmitError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [createdProject, setCreatedProject] = useState<Project | null>(null);

  useEffect(() => {
    let cancelled = false;
    void getAnduinClient().listSources({})
      .then((response) => {
        if (!cancelled) setSources(response.sources);
      })
      .catch((error: unknown) => {
        if (!cancelled) {
          setSourceError(error instanceof Error ? error.message : "Could not load data sources.");
        }
      })
      .finally(() => {
        if (!cancelled) setLoadingSources(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  function toggleSource(id: string) {
    setSelectedSourceIds((current) =>
      current.includes(id) ? current.filter((sourceId) => sourceId !== id) : [...current, id],
    );
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitError("");
    const formData = new FormData(event.currentTarget);
    const name = String(formData.get("name") ?? "").trim();
    const goal = String(formData.get("goal") ?? "").trim();
    if (!selectedSourceIds.length) {
      setSubmitError("Select at least one data source for this project.");
      return;
    }

    setSubmitting(true);
    try {
      const response = await getAnduinClient().createProject({
        name,
        goal,
        sourceIds: selectedSourceIds,
      });
      if (!response.project) throw new Error("The API created no project response.");
      setCreatedProject(response.project);
    } catch (error) {
      setSubmitError(error instanceof Error ? error.message : "Could not create this project.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div
      className="project-dialog-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && !submitting) onClose();
      }}
    >
      <section className="project-dialog" role="dialog" aria-modal="true" aria-labelledby="new-project-heading">
        <div className="project-dialog-heading">
          <div>
            <span className="eyebrow">PROJECT WORKSPACE</span>
            <h2 id="new-project-heading">{createdProject ? "Project created" : "Start a project"}</h2>
            <p>{createdProject ? "Your project is saved and linked to its sources." : "Give your question a focus and choose the data it should use."}</p>
          </div>
          <button className="project-dialog-close" type="button" onClick={onClose} aria-label="Close dialog">×</button>
        </div>

        {createdProject ? (
          <div className="project-created-state" role="status">
            <span className="project-created-check" aria-hidden="true">✓</span>
            <div>
              <strong>{createdProject.name}</strong>
              <p>{createdProject.goal}</p>
              <small>{createdProject.sourceIds.length} {createdProject.sourceIds.length === 1 ? "source" : "sources"} connected · Project ID {createdProject.id}</small>
            </div>
            <p className="project-setup-note">
              Workflow generation is not enabled yet, so this project does not have an analysis run yet.
            </p>
            <div className="project-dialog-actions">
              <button type="button" className="button button-primary" onClick={onClose}>Back to dashboard</button>
            </div>
          </div>
        ) : (
          <form className="project-form" onSubmit={submit}>
            <label className="project-field">
              <span>Project name</span>
              <input name="name" required maxLength={160} placeholder="e.g. City mobility & weather" autoFocus />
            </label>
            <label className="project-field">
              <span>What do you want to understand?</span>
              <textarea name="goal" required rows={3} placeholder="Describe the question you want to investigate." />
            </label>

            <fieldset className="project-source-fieldset">
              <legend>Choose source data</legend>
              <p>Select one or more sources to connect to this project.</p>
              {loadingSources ? (
                <div className="project-source-status"><i className="project-spinner" /> Loading sources…</div>
              ) : sourceError ? (
                <div className="project-form-error" role="alert">{sourceError}</div>
              ) : sources.length === 0 ? (
                <div className="project-source-empty">
                  <strong>No sources available yet</strong>
                  <span>Connect an RSS, web, or CSV source before creating a project.</span>
                  <button type="button" className="text-link" onClick={onClose}>Go to sources <span aria-hidden="true">→</span></button>
                </div>
              ) : (
                <div className="project-source-list">
                  {sources.map((source) => (
                    <label
                      className={`project-source-option ${selectedSourceIds.includes(source.id) ? "is-selected" : ""}`}
                      key={source.id}
                    >
                      <input
                        type="checkbox"
                        checked={selectedSourceIds.includes(source.id)}
                        onChange={() => toggleSource(source.id)}
                      />
                      <span className="project-source-check" aria-hidden="true">✓</span>
                      <span className="project-source-copy">
                        <strong>{source.name}</strong>
                        <small>{sourceKindLabel(source.kind)} · {source.recordCount.toLocaleString()} records</small>
                      </span>
                    </label>
                  ))}
                </div>
              )}
            </fieldset>

            {submitError && <p className="project-form-error" role="alert">{submitError}</p>}

            <div className="project-dialog-actions">
              <button type="button" className="button button-outline" onClick={onClose} disabled={submitting}>Cancel</button>
              <button type="submit" className="button button-primary" disabled={submitting || loadingSources || !sources.length}>
                {submitting ? <><i className="project-spinner" /> Creating…</> : "Create project"}
              </button>
            </div>
          </form>
        )}
      </section>
    </div>
  );
}
