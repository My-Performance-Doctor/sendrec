import { useEffect, useState } from "react";
import { apiFetch } from "../../api/client";

// The preview page renews its media URL using the bound staff session. Public
// watch links are never used to preview unpublished recordings.
export function ManagedPreview({ id, mediaVersion }: { id: string; mediaVersion?: number }) {
  const [url, setUrl] = useState("");
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let cancelled = false;
    setUrl(""); setError("");
    apiFetch<{ previewUrl: string }>(`/api/videos/${id}/preview`, {
      method: "POST",
      body: JSON.stringify({ origin: window.location.origin, nonce: crypto.randomUUID() }),
    }).then(result => {
      if (!result) throw new Error("Preview unavailable");
      const target = new URL(result.previewUrl, window.location.origin);
      if (target.origin !== window.location.origin || target.pathname !== "/mpd-preview") throw new Error("Preview unavailable");
      if (!cancelled) setUrl(target.href);
    }).catch(err => {
      if (!cancelled) setError(err instanceof Error ? err.message : "Preview unavailable");
    });
    return () => { cancelled = true; };
  }, [id, mediaVersion, attempt]);
  if (error) return <div role="alert"><p>{error}</p><button className="detail-btn" onClick={() => setAttempt(attempt + 1)}>Retry preview</button></div>;
  if (!url) return <p role="status">Loading staff preview...</p>;
  return <iframe title="Staff preview" src={url} className="video-detail-thumbnail" allow="fullscreen" allowFullScreen style={{ border: 0, width: "100%", aspectRatio: "16/9" }} />;
}
