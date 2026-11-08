import NewProjectLauncher from "@/components/new-project-launcher";

function NavigationIcon({ kind }: { kind: "overview" | "sources" | "findings" | "plan" }) {
  if (kind === "overview") {
    return (
      <svg viewBox="0 0 20 20" aria-hidden="true">
        <rect x="2.5" y="2.5" width="6" height="6" rx="1.5" />
        <rect x="11.5" y="2.5" width="6" height="6" rx="1.5" />
        <rect x="2.5" y="11.5" width="6" height="6" rx="1.5" />
        <rect x="11.5" y="11.5" width="6" height="6" rx="1.5" />
      </svg>
    );
  }

  if (kind === "sources") {
    return (
      <svg viewBox="0 0 20 20" aria-hidden="true">
        <ellipse cx="10" cy="5" rx="6.5" ry="2.5" />
        <path d="M3.5 5v5c0 1.4 2.9 2.5 6.5 2.5s6.5-1.1 6.5-2.5V5M3.5 10v5c0 1.4 2.9 2.5 6.5 2.5s6.5-1.1 6.5-2.5v-5" />
      </svg>
    );
  }

  if (kind === "findings") {
    return (
      <svg viewBox="0 0 20 20" aria-hidden="true">
        <path d="M3 15.5 7.2 11l3 2.2 6.8-8" />
        <path d="M12.8 5.2H17v4.2" />
      </svg>
    );
  }

  return (
    <svg viewBox="0 0 20 20" aria-hidden="true">
      <path d="M5 2.8h6.5L16 7.3v9.9H5z" />
      <path d="M11.5 2.8v4.5H16M8 11h5M8 14h5" />
    </svg>
  );
}

function CorrelationChart() {
  return (
    <div className="chart-frame">
      <div className="chart-heading">
        <div>
          <span className="eyebrow">PATTERN OVER TIME</span>
          <p>Daily values, normalised</p>
        </div>
        <span className="chart-period">180 days</span>
      </div>
      <svg
        className="line-chart"
        viewBox="0 0 520 190"
        role="img"
        aria-label="Illustrative chart showing bike hire volume and temperature following similar patterns"
      >
        <defs>
          <linearGradient id="chartFill" x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" stopColor="#82d7bd" stopOpacity="0.22" />
            <stop offset="100%" stopColor="#82d7bd" stopOpacity="0" />
          </linearGradient>
        </defs>
        <path className="chart-gridline" d="M34 24H510M34 61H510M34 98H510M34 135H510M34 172H510" />
        <path className="chart-fill" d="M34 126 68 114 102 125 136 88 170 98 204 70 238 84 272 55 306 67 340 38 374 53 408 33 442 48 476 26 510 34V172H34Z" />
        <path className="chart-line chart-line-rides" d="M34 126 68 114 102 125 136 88 170 98 204 70 238 84 272 55 306 67 340 38 374 53 408 33 442 48 476 26 510 34" />
        <path className="chart-line chart-line-weather" d="M34 142 68 132 102 117 136 110 170 104 204 91 238 83 272 75 306 63 340 59 374 51 408 45 442 38 476 32 510 27" />
        <circle className="chart-point" cx="510" cy="34" r="4" />
        <text x="34" y="187">EARLIER</text>
        <text x="510" y="187" textAnchor="end">MORE RECENT</text>
      </svg>
      <div className="chart-legend">
        <span><i className="legend-dot rides" /> Bike hires</span>
        <span><i className="legend-dot weather" /> Temperature</span>
        <span className="chart-caption">Illustrative fixture</span>
      </div>
    </div>
  );
}

function TrendChart() {
  return (
    <div className="mini-chart-frame">
      <div className="mini-chart-labels"><span>LOWER RAINFALL</span><span>HIGHER RAINFALL</span></div>
      <svg
        className="mini-chart"
        viewBox="0 0 520 105"
        role="img"
        aria-label="Illustrative chart showing lower bike hire counts on wetter days"
      >
        <path className="chart-gridline" d="M12 18H508M12 50H508M12 82H508" />
        <path className="mini-trend-area" d="M12 24 52 30 92 27 132 38 172 35 212 43 252 48 292 46 332 59 372 56 412 68 452 72 492 81 508 78V96H12Z" />
        <path className="mini-trend-line" d="M12 24 52 30 92 27 132 38 172 35 212 43 252 48 292 46 332 59 372 56 412 68 452 72 492 81 508 78" />
        <circle className="mini-trend-point" cx="508" cy="78" r="4" />
      </svg>
      <div className="chart-legend mini-legend">
        <span><i className="legend-dot rides" /> Bike hires by rainfall band</span>
        <span className="chart-caption">Illustrative fixture</span>
      </div>
    </div>
  );
}

export default function Home() {
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <a className="brand" href="#project" aria-label="Anduin home">
          <span className="brand-mark" aria-hidden="true">
            <svg viewBox="0 0 30 30">
              <path d="M5 19.5 10.3 14l4 3.2L25 6.5" />
              <path d="M19.5 6.5H25V12" />
              <circle cx="5" cy="19.5" r="1.5" />
            </svg>
          </span>
          <span className="brand-copy"><strong>anduin</strong><small>INSIGHT STUDIO</small></span>
        </a>

        <div className="workspace-switcher">
          <span className="workspace-avatar">H</span>
          <span className="workspace-copy"><small>WORKSPACE</small><strong>Hack the Borough</strong></span>
          <span className="workspace-chevron" aria-hidden="true">⌄</span>
        </div>

        <nav className="sidebar-nav" aria-label="Main navigation">
          <p className="nav-label">WORKSPACE</p>
          <a className="nav-link active" href="#project">
            <NavigationIcon kind="overview" /> <span>Overview</span>
          </a>
          <a className="nav-link" href="#sources">
            <NavigationIcon kind="sources" /> <span>Sources</span><span className="nav-count">3</span>
          </a>
          <a className="nav-link" href="#findings">
            <NavigationIcon kind="findings" /> <span>Findings</span><span className="nav-count">2</span>
          </a>
          <p className="nav-label nav-label-spaced">PROJECT</p>
          <a className="nav-link" href="#analysis-plan">
            <NavigationIcon kind="plan" /> <span>Analysis plan</span>
          </a>
        </nav>

        <div className="sidebar-bottom">
          <div className="system-status"><span className="status-dot" /> DEMO WORKSPACE</div>
          <p>Insights that hold up.</p>
          <span>Built for curious people in Hackney.</span>
        </div>
      </aside>

      <div className="mobile-header">
        <a className="brand" href="#project">
          <span className="brand-mark" aria-hidden="true"><svg viewBox="0 0 30 30"><path d="M5 19.5 10.3 14l4 3.2L25 6.5" /><path d="M19.5 6.5H25V12" /><circle cx="5" cy="19.5" r="1.5" /></svg></span>
          <span className="brand-copy"><strong>anduin</strong><small>INSIGHT STUDIO</small></span>
        </a>
        <span className="mobile-demo-label"><i className="status-dot" /> DEMO PREVIEW</span>
      </div>

      <main className="main-content" id="project">
        <header className="topbar">
          <div className="breadcrumbs"><span>Projects</span><span className="crumb-divider">/</span><strong>City mobility &amp; weather</strong></div>
          <div className="topbar-actions">
            <span className="preview-badge"><span className="preview-dot" /> UI PREVIEW</span>
            <NewProjectLauncher />
            <a className="button button-outline" href="#analysis-plan">Review analysis plan <span aria-hidden="true">↗</span></a>
          </div>
        </header>

        <div className="page-content">
          <section className="project-heading">
            <div className="project-title-block">
              <div className="eyebrow project-eyebrow">PROJECT OVERVIEW <span className="eyebrow-divider">/</span> DEMO DATASET</div>
              <h1>City mobility <span>&amp;</span> weather</h1>
              <p className="project-goal">Understand how local weather patterns relate to daily bike-hire demand.</p>
            </div>
            <div className="last-run">
              <span className="last-run-icon" aria-hidden="true">✓</span>
              <div><strong>Example run complete</strong><span>Updated moments ago</span></div>
            </div>
          </section>

          <section className="source-row" id="sources" aria-labelledby="sources-heading">
            <div className="source-row-heading">
              <div><span className="eyebrow">CONNECTED SOURCES</span><h2 id="sources-heading">A fuller picture, from three feeds</h2></div>
              <a className="text-link" href="#sources-list">View sources <span aria-hidden="true">→</span></a>
            </div>
            <div className="source-list" id="sources-list">
              <div className="source-item"><span className="source-icon source-csv">CSV</span><span><strong>Bike hire activity</strong><small>Daily counts · 180 days</small></span><i className="source-check">✓</i></div>
              <div className="source-item"><span className="source-icon source-weather">°</span><span><strong>Local weather</strong><small>Temperature &amp; rainfall</small></span><i className="source-check">✓</i></div>
              <div className="source-item"><span className="source-icon source-rss">RSS</span><span><strong>City news feed</strong><small>Contextual headlines</small></span><i className="source-check">✓</i></div>
            </div>
          </section>

          <section className="funnel-section" aria-labelledby="funnel-heading">
            <div className="section-heading">
              <div><span className="eyebrow">LATEST ANALYSIS</span><h2 id="funnel-heading">Evidence, not just noise</h2></div>
              <span className="run-version"><span className="run-version-dot" /> WORKFLOW V3 <span className="version-separator">·</span> DEMO FIXTURE</span>
            </div>
            <div className="funnel-card">
              <div className="funnel-intro">
                <div><span className="funnel-kicker">THE EVIDENCE FUNNEL</span><h3>Most patterns don’t make the cut.</h3></div>
                <span className="fdr-chip"><span aria-hidden="true">⌁</span> False discovery control <b>q ≤ 0.10</b></span>
              </div>
              <div className="funnel-steps">
                <div className="funnel-step step-tested"><strong>330</strong><span>patterns tested</span><div className="step-track"><i style={{ width: "100%" }} /></div></div>
                <div className="funnel-step step-screened"><strong>41</strong><span>under p &lt; 0.05</span><div className="step-track"><i style={{ width: "68%" }} /></div></div>
                <div className="funnel-step step-passed"><strong>3</strong><span>passed FDR</span><div className="step-track"><i style={{ width: "38%" }} /></div></div>
                <div className="funnel-step step-stable"><strong>2</strong><span>stable in both halves</span><div className="step-track"><i style={{ width: "23%" }} /></div></div>
              </div>
              <div className="funnel-footnote"><span className="footnote-icon">i</span> The stats engine filters weak signals before any explanation is written. <span className="fixture-note">Illustrative fixture values</span></div>
            </div>
          </section>

          <section className="findings-section" id="findings" aria-labelledby="findings-heading">
            <div className="section-heading findings-heading">
              <div><span className="eyebrow">VERIFIED INSIGHTS</span><h2 id="findings-heading">What stands out</h2></div>
              <span className="verified-count"><span>✓</span> 2 findings held up</span>
            </div>

            <div className="findings-grid">
              <article className="finding-card featured-finding">
                <div className="finding-copy">
                  <div className="finding-label-row"><span className="finding-kind"><i className="kind-spark" /> LAGGED CORRELATION</span><span className="verified-label"><span>✓</span> VERIFIED</span></div>
                  <h3>Warmer days are associated with higher bike-hire volume</h3>
                  <p className="finding-explanation">Bike activity tends to move with temperature across the observed period. The relationship stays in the same direction when the data is checked in two halves.</p>
                  <div className="stats-row">
                    <div className="stat"><strong>+0.62</strong><span>Spearman ρ</span></div>
                    <div className="stat"><strong>+1 day</strong><span>Best lag</span></div>
                    <div className="stat"><strong>180</strong><span>Observations</span></div>
                    <div className="stat"><strong>0.03</strong><span>Adjusted q</span></div>
                  </div>
                  <p className="caveat"><span aria-hidden="true">↗</span> Association, not causation. Fixture values shown for demo purposes.</p>
                </div>
                <CorrelationChart />
                <div className="finding-footer"><span><span className="footer-check">✓</span> Holds in both halves</span><a href="#analysis-plan">How we checked it <span aria-hidden="true">→</span></a></div>
              </article>

              <article className="finding-card secondary-finding">
                <div className="finding-label-row"><span className="finding-kind finding-kind-violet"><i className="kind-rain" /> WEATHER SIGNAL</span><span className="verified-label"><span>✓</span> VERIFIED</span></div>
                <h3>Rainfall and bike hire move in opposite directions</h3>
                <p className="finding-explanation">Wetter periods line up with quieter hire activity in this sample. The pattern remains visible after the stability check.</p>
                <TrendChart />
                <div className="secondary-stats">
                  <span><strong>−0.48</strong> Spearman ρ</span><span><strong>0.08</strong> Adjusted q</span><span><strong>n = 180</strong></span>
                </div>
                <p className="secondary-caveat">Association, not causation · illustrative fixture</p>
              </article>

              <details className="analysis-card" id="analysis-plan">
                <summary>
                  <span className="plan-icon"><NavigationIcon kind="plan" /></span>
                  <span className="plan-title"><span className="eyebrow">AGENT-AUTHORED WORKFLOW · V3</span><strong>Analysis plan</strong><small>What was tested, and why</small></span>
                  <span className="details-chevron" aria-hidden="true">⌄</span>
                </summary>
                <div className="analysis-details">
                  <p>Compare daily weather measures with bike-hire counts, looking for lagged relationships, unusual periods and meaningful changes in trend.</p>
                  <dl>
                    <div><dt>Methods</dt><dd>Spearman correlation · lag scan</dd></div>
                    <div><dt>Time grain</dt><dd>Daily</dd></div>
                    <div><dt>Noise control</dt><dd>Benjamini–Hochberg · q ≤ 0.10</dd></div>
                    <div><dt>Stability</dt><dd>Split-half agreement · minimum overlap 20</dd></div>
                  </dl>
                  <p className="plan-disclaimer">The analysis engine computes every metric. The agent writes explanations only.</p>
                </div>
              </details>
            </div>
          </section>

          <footer className="page-footer">
            <span><span className="footer-mark">a</span> ANDUIN <span className="footer-divider">/</span> A SMALL TOOL FOR BIG QUESTIONS</span>
            <span>Demo interface · Illustrative fixture data</span>
          </footer>
        </div>
      </main>
    </div>
  );
}
